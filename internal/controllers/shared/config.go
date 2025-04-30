package shared

import (
	"context"
	"fmt"
	"reflect"
	"sync"

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	ctrlutils "github.com/openmcp-project/controller-utils/pkg/controller"

	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/event"

	"k8s.io/apimachinery/pkg/util/sets"
	toolscache "k8s.io/client-go/tools/cache"

	clustersv1alpha1 "github.com/openmcp-project/cluster-provider-gardener/api/clusters/v1alpha1"
	providerv1alpha1 "github.com/openmcp-project/cluster-provider-gardener/api/core/v1alpha1"
	gardenv1beta1 "github.com/openmcp-project/cluster-provider-gardener/api/external/gardener/pkg/apis/core/v1beta1"
)

var (
	environment  string
	providerName string
)

func SetEnvironment(env string) {
	if environment != "" {
		panic("environment already set")
	}
	environment = env
}

func Environment() string {
	if environment == "" {
		panic("environment not set")
	}
	return environment
}

func SetProviderName(name string) {
	if providerName != "" {
		panic("provider name already set")
	}
	providerName = name
}

func ProviderName() string {
	if providerName == "" {
		panic("providerName not set")
	}
	return providerName
}

// RuntimeConfiguration is a struct that holds the loaded ProviderConfigurations, enriched with further information gathered during runtime.
// For each instance of the ClusterProvider that is running, there should be one instance of this struct.
// It is important that it is shared between the ProviderConfiguration and the Cluster controllers. The former one will fill it with information that the latter one can use.
type RuntimeConfiguration struct {
	Lock *sync.RWMutex

	// providerConfigurations is a map of all providerConfigurations that are currently loaded, with their names as keys.
	providerConfigurations map[string]*providerv1alpha1.ProviderConfig
	// landscapes is a map of all Gardener landscapes mentioned in all ProviderConfigurations.
	// The landscape names are expected to be either unique or refer to the same landscape in case of the same name across all ProviderConfigurations.
	landscapes map[string]*Landscape
	// profiles is a map of all profiles derived from all ProviderConfigurations.
	// The name of the k8s resource is used as key.
	profiles map[string]*Profile

	// not modified after creation
	ShootWatch        chan event.TypedGenericEvent[*gardenv1beta1.Shoot]
	PlatformCluster   *clusters.Cluster
	OnboardingCluster *clusters.Cluster
}

func NewRuntimeConfiguration(platform, onboarding *clusters.Cluster) *RuntimeConfiguration {
	return &RuntimeConfiguration{
		Lock:              &sync.RWMutex{},
		ShootWatch:        make(chan event.TypedGenericEvent[*gardenv1beta1.Shoot], 1024),
		PlatformCluster:   platform,
		OnboardingCluster: onboarding,
	}
}

func (rc *RuntimeConfiguration) GetProviderConfigurations() map[string]*providerv1alpha1.ProviderConfig {
	if rc.providerConfigurations == nil {
		return nil
	}
	res := make(map[string]*providerv1alpha1.ProviderConfig, len(rc.providerConfigurations))
	for k, v := range rc.providerConfigurations {
		res[k] = v.DeepCopy()
	}
	return res
}

func (rc *RuntimeConfiguration) GetLandscape(name string) *Landscape {
	if rc.landscapes == nil {
		return nil
	}
	ls := rc.landscapes[name]
	if ls == nil {
		return nil
	}
	return ls.DeepCopy()
}

func (rc *RuntimeConfiguration) GetLandscapes() map[string]*Landscape {
	if rc.landscapes == nil {
		return nil
	}
	res := make(map[string]*Landscape, len(rc.landscapes))
	for k, v := range rc.landscapes {
		res[k] = v.DeepCopy()
	}
	return res
}

func (rc *RuntimeConfiguration) GetProfile(k8sName string) *Profile {
	if rc.profiles == nil {
		return nil
	}
	profile := rc.profiles[k8sName]
	if profile == nil {
		return nil
	}
	return profile.DeepCopy()
}

func (rc *RuntimeConfiguration) SetProviderConfigurations(providerConfigurations map[string]*providerv1alpha1.ProviderConfig) {
	rc.providerConfigurations = make(map[string]*providerv1alpha1.ProviderConfig, len(providerConfigurations))
	for k, v := range providerConfigurations {
		rc.providerConfigurations[k] = v.DeepCopy()
	}
}

func (rc *RuntimeConfiguration) SetProviderConfiguration(name string, providerConfiguration *providerv1alpha1.ProviderConfig) {
	if rc.providerConfigurations == nil {
		rc.providerConfigurations = make(map[string]*providerv1alpha1.ProviderConfig)
	}
	rc.providerConfigurations[name] = providerConfiguration.DeepCopy()
}

func (rc *RuntimeConfiguration) UnsetProviderConfiguration(name string) {
	if rc.providerConfigurations == nil {
		return
	}
	delete(rc.providerConfigurations, name)
}

func (rc *RuntimeConfiguration) SetLandscape(ctx context.Context, ls *Landscape) error {
	if rc.landscapes == nil {
		rc.landscapes = make(map[string]*Landscape)
	}
	if err := rc.UnsetLandscape(ctx, ls.Name); err != nil {
		return fmt.Errorf("error removing old landscape: %w", err)
	}
	ctx, ls.stop = context.WithCancel(ctx)
	if ls.Cluster != nil {
		// construct a watch for the shoot resources in this landscape
		enqueueReconcile := func(sh *gardenv1beta1.Shoot) {
			rc.ShootWatch <- event.TypedGenericEvent[*gardenv1beta1.Shoot]{
				Object: sh,
			}
		}
		inf, err := ls.Cluster.Cluster().GetCache().GetInformer(ctx, &gardenv1beta1.Shoot{}, cache.BlockUntilSynced(true))
		if err != nil {
			return fmt.Errorf("error getting shoot informer: %w", err)
		}
		if _, err := inf.AddEventHandler(toolscache.FilteringResourceEventHandler{
			FilterFunc: func(obj any) bool {
				shoot, ok := obj.(*gardenv1beta1.Shoot)
				if !ok {
					return false
				}
				environment, ok := ctrlutils.GetLabel(shoot, providerv1alpha1.ClusterReferenceLabelEnvironment)
				if !ok || environment != Environment() {
					return false
				}
				provider, ok := ctrlutils.GetLabel(shoot, providerv1alpha1.ClusterReferenceLabelProvider)
				if !ok || provider != ProviderName() {
					return false
				}
				return true
			},
			Handler: toolscache.ResourceEventHandlerFuncs{
				UpdateFunc: func(oldObj, newObj any) {
					oldShoot, ok := oldObj.(*gardenv1beta1.Shoot)
					if !ok {
						return
					}
					newShoot, ok := newObj.(*gardenv1beta1.Shoot)
					if !ok {
						return
					}
					// since the state of our Cluster is based purely on the shoot's conditions,
					// only trigger a reconciliation, if the conditions changed
					changed := false
					for _, newCon := range newShoot.Status.Conditions {
						for _, oldCon := range oldShoot.Status.Conditions {
							if newCon.Type != oldCon.Type {
								continue
							}
							changed = reflect.DeepEqual(newCon, oldCon)
							break
						}
						if changed {
							break
						}
					}
					if changed {
						enqueueReconcile(newShoot)
					}
				},
				DeleteFunc: func(obj any) {
					shoot, ok := obj.(*gardenv1beta1.Shoot)
					if !ok {
						return
					}
					enqueueReconcile(shoot)
				},
			},
		}); err != nil {
			return fmt.Errorf("error adding event handler to shoot informer: %w", err)
		}
		// this feels really hacky and it hides the error that potentially comes out of Start()
		go ls.Cluster.Cluster().Start(ctx)
	}
	rc.landscapes[ls.Name] = ls
	return nil
}

// UpdateLandscapeResource just updates the Landscape resource in the internal Landscape object.
func (rc *RuntimeConfiguration) UpdateLandscapeResource(obj *providerv1alpha1.Landscape) {
	if rc.landscapes == nil {
		return
	}
	if ls, ok := rc.landscapes[obj.Name]; ok {
		ls.Resource = obj.DeepCopy()
	}
}

// UnsetLandscape removes the shoot informer for the landscape and deletes it from the internal Landscapes list, if that was successful.
func (rc *RuntimeConfiguration) UnsetLandscape(ctx context.Context, name string) error {
	if rc.landscapes == nil {
		return nil
	}
	old := rc.landscapes[name]
	if old != nil && old.Cluster != nil {
		old.stop() // stop the watch
		if err := old.Cluster.Cluster().GetCache().RemoveInformer(ctx, &gardenv1beta1.Shoot{}); err != nil {
			return fmt.Errorf("error removing shoot informer: %w", err)
		}
	}
	delete(rc.landscapes, name)
	return nil
}

func (rc *RuntimeConfiguration) SetProfilesForProviderConfiguration(providerConfigurationName string, profiles ...*Profile) {
	if rc.profiles == nil {
		rc.profiles = make(map[string]*Profile)
	}
	seenProfileNames := sets.New[string]()
	for _, profile := range profiles {
		resName := ProfileK8sName(profile.GetName())
		seenProfileNames.Insert(resName)
		if profile.ProviderConfigSource == "" {
			profile.ProviderConfigSource = providerConfigurationName
		}
		rc.profiles[resName] = profile.DeepCopy()
	}
	// remove profiles for this provider configuration that have been added previously but are not in the current set
	for k, v := range rc.profiles {
		if v.ProviderConfigSource == providerConfigurationName && !seenProfileNames.Has(k) {
			delete(rc.profiles, k)
		}
	}
}

func (rc *RuntimeConfiguration) GetProfilesForProviderConfiguration(providerConfigurationName string) map[string]*Profile {
	if rc.profiles == nil {
		return nil
	}
	res := map[string]*Profile{}
	for k, v := range rc.profiles {
		if v.ProviderConfigSource == providerConfigurationName {
			res[k] = v.DeepCopy()
		}
	}
	return res
}

func (rc *RuntimeConfiguration) UnsetProfilesForProviderConfiguration(providerConfigurationName string) {
	if rc.profiles == nil {
		return
	}
	for k, v := range rc.profiles {
		if v.ProviderConfigSource == providerConfigurationName {
			delete(rc.profiles, k)
		}
	}
}

type Landscape struct {
	Name     string
	Cluster  *clusters.Cluster
	Resource *providerv1alpha1.Landscape
	stop     context.CancelFunc
}

func (l *Landscape) Available() bool {
	return l != nil && l.Resource != nil && (l.Resource.Status.Phase == providerv1alpha1.LANDSCAPE_PHASE_AVAILABLE || l.Resource.Status.Phase == providerv1alpha1.LANDSCAPE_PHASE_PARTIALLY_AVAILABLE)
}

func (l *Landscape) Projects() []providerv1alpha1.ProjectData {
	if l == nil || l.Resource == nil {
		return nil
	}
	projects := make([]providerv1alpha1.ProjectData, len(l.Resource.Status.Projects))
	copy(projects, l.Resource.Status.Projects)
	return projects
}

func (l *Landscape) DeepCopy() *Landscape {
	return &Landscape{
		Name:     l.Name,
		Cluster:  l.Cluster,
		Resource: l.Resource.DeepCopy(),
		stop:     l.stop,
	}
}

type Profile struct {
	RuntimeData
	Config               *providerv1alpha1.GardenerConfiguration
	ProviderConfigSource string
}

// RuntimeData holds information that has been loaded during runtime.
// It belongs to a specific Gardener configuration.
// This applies, for example, to every information received by reading a Gardener CloudProfile.
type RuntimeData struct {
	Project              providerv1alpha1.ProjectData
	SupportedK8sVersions []K8sVersion
}

type K8sVersion struct {
	Version    string
	Deprecated bool
}

func (v *K8sVersion) ToResourceRepresentation() *clustersv1alpha1.SupportedK8sVersion {
	return &clustersv1alpha1.SupportedK8sVersion{
		Version:    v.Version,
		Deprecated: v.Deprecated,
	}
}

func (v *K8sVersion) DeepCopy() *K8sVersion {
	return &K8sVersion{
		Version:    v.Version,
		Deprecated: v.Deprecated,
	}
}

func (rd *RuntimeData) DeepCopy() *RuntimeData {
	versions := make([]K8sVersion, len(rd.SupportedK8sVersions))
	for i, v := range rd.SupportedK8sVersions {
		versions[i] = *v.DeepCopy()
	}
	return &RuntimeData{
		SupportedK8sVersions: versions,
		Project:              rd.Project,
	}
}

func (p *Profile) DeepCopy() *Profile {
	return &Profile{
		RuntimeData:          *p.RuntimeData.DeepCopy(),
		Config:               p.Config.DeepCopy(),
		ProviderConfigSource: p.ProviderConfigSource,
	}
}

func (p *Profile) GetName() string {
	return fmt.Sprintf("%s/%s", p.Config.LandscapeRef.Name, p.Config.Name)
}

func ProfileK8sName(profileName string) string {
	return ctrlutils.K8sNameHash(Environment(), ProviderName(), profileName)
}

func ShootK8sName(clusterName, clusterNamespace, projectName string) string {
	return ctrlutils.K8sNameHash(clusterNamespace, clusterName)[:(21 - len(projectName))]
}
func ShootK8sNameFromCluster(c *clustersv1alpha1.Cluster, projectName string) string {
	return ShootK8sName(c.Name, c.Namespace, projectName)
}
