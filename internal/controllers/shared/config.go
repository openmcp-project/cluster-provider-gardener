package shared

import (
	"context"
	"fmt"
	"reflect"
	"sync"

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	ctrlutils "github.com/openmcp-project/controller-utils/pkg/controller"
	"github.com/openmcp-project/controller-utils/pkg/logging"
	"github.com/openmcp-project/controller-utils/pkg/threads"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/event"

	toolscache "k8s.io/client-go/tools/cache"

	clustersv1alpha1 "github.com/openmcp-project/openmcp-operator/api/clusters/v1alpha1"
	commonapi "github.com/openmcp-project/openmcp-operator/api/common"

	providerv1alpha1 "github.com/openmcp-project/cluster-provider-gardener/api/core/v1alpha1"
	gardenv1beta1 "github.com/openmcp-project/cluster-provider-gardener/api/external/gardener/pkg/apis/core/v1beta1"
)

var (
	environment                          string
	providerName                         string
	accessRequestServiceAccountNamespace string
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
		panic("provider name not set")
	}
	return providerName
}

func SetAccessRequestServiceAccountNamespace(ns string) {
	if accessRequestServiceAccountNamespace != "" {
		panic("accessrequest namespace already set")
	}
	accessRequestServiceAccountNamespace = ns
}

func AccessRequestServiceAccountNamespace() string {
	if accessRequestServiceAccountNamespace == "" {
		panic("accessrequest namespace not set")
	}
	return accessRequestServiceAccountNamespace
}

// RuntimeConfiguration is a struct that holds the loaded ProviderConfigurations, enriched with further information gathered during runtime.
// It is important that the same instance is shared between the different controllers that make up the cluster provider.
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

	// not modified after creation or thread-safe
	ShootWatchManager       *threads.ThreadManager
	ShootWatch              chan event.TypedGenericEvent[*gardenv1beta1.Shoot]             // changes on the watched shoots are sent to the Cluster controller via this channel
	ReconcileLandscape      chan event.TypedGenericEvent[*providerv1alpha1.Landscape]      // sending a Landscape to this channel will trigger a reconciliation of the corresponding resource
	ReconcileProviderConfig chan event.TypedGenericEvent[*providerv1alpha1.ProviderConfig] // sending a ProviderConfig to this channel will trigger a reconciliation of the corresponding resource
	ReconcileCluster        chan event.TypedGenericEvent[*clustersv1alpha1.Cluster]        // sending a Cluster to this channel will trigger a reconciliation of the corresponding resource
	PlatformCluster         *clusters.Cluster
}

func NewRuntimeConfiguration(platform *clusters.Cluster, swMgr *threads.ThreadManager) *RuntimeConfiguration {
	return &RuntimeConfiguration{
		Lock:                    &sync.RWMutex{},
		ShootWatch:              make(chan event.TypedGenericEvent[*gardenv1beta1.Shoot], 1024),
		ReconcileLandscape:      make(chan event.TypedGenericEvent[*providerv1alpha1.Landscape], 1024),
		ReconcileProviderConfig: make(chan event.TypedGenericEvent[*providerv1alpha1.ProviderConfig], 1024),
		ReconcileCluster:        make(chan event.TypedGenericEvent[*clustersv1alpha1.Cluster], 1024),
		ShootWatchManager:       swMgr,
		PlatformCluster:         platform,
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

func (rc *RuntimeConfiguration) GetLandscapeForProfile(profile *Profile) *Landscape {
	if rc.landscapes == nil {
		return nil
	}
	if profile == nil || profile.ProviderConfig == nil {
		return nil
	}
	ls := rc.landscapes[profile.ProviderConfig.Spec.LandscapeRef.Name]
	if ls == nil {
		return nil
	}
	return ls.DeepCopy()
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

func (rc *RuntimeConfiguration) GetProfileAndLandscape(profileK8sName string) (*Profile, *Landscape) {
	profile := rc.GetProfile(profileK8sName)
	landscape := rc.GetLandscapeForProfile(profile)
	return profile, landscape
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
	if ls.Cluster != nil && rc.ShootWatchManager != nil {
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
		// start the watch
		rc.ShootWatchManager.Run(ctx, ls.Name, ls.Cluster.Cluster().Start, func(ctx context.Context, tr threads.ThreadReturn) {
			// reconcile the Landscape resource, if the watch ends with an error
			if tr.Err != nil {
				log := logging.FromContextOrDiscard(ctx)
				log.Error(tr.Err, "shoot watch failed, triggering Landscape reconciliation")
				rc.ReconcileLandscape <- event.TypedGenericEvent[*providerv1alpha1.Landscape]{Object: ls.Resource} // doesn't matter if outdated, we just need the name
			}
		})
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

func (rc *RuntimeConfiguration) SetProfileForProviderConfiguration(providerConfigName string, profile *Profile) {
	if rc.profiles == nil {
		rc.profiles = make(map[string]*Profile)
	}
	pKey := ProfileK8sName(providerConfigName)
	if profile == nil {
		delete(rc.profiles, pKey)
		return
	}
	rc.profiles[pKey] = profile.DeepCopy()
}

func (rc *RuntimeConfiguration) GetProfileForProviderConfiguration(providerConfigName string) *Profile {
	if rc.profiles == nil {
		return nil
	}
	pKey := ProfileK8sName(providerConfigName)
	profile := rc.profiles[pKey]
	if profile == nil {
		return nil
	}
	return profile.DeepCopy()
}

func (rc *RuntimeConfiguration) UnsetProfilesForProviderConfiguration(providerConfigName string) {
	rc.SetProfileForProviderConfiguration(providerConfigName, nil)
}

type Landscape struct {
	Name     string
	Cluster  *clusters.Cluster
	Resource *providerv1alpha1.Landscape
	stop     context.CancelFunc
}

func (l *Landscape) Available() bool {
	if l == nil || l.Resource == nil {
		return false
	}
	if l.Resource.Status.Phase == commonapi.StatusPhaseReady {
		return true
	}
	// check conditions if at least one is true
	for _, con := range l.Resource.Status.Conditions {
		if con.Status == metav1.ConditionTrue {
			return true
		}
	}
	return false
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
	ProviderConfig *providerv1alpha1.ProviderConfig
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
		RuntimeData:    *p.RuntimeData.DeepCopy(),
		ProviderConfig: p.ProviderConfig.DeepCopy(),
	}
}

func ProfileK8sName(providerConfigName string) string {
	return fmt.Sprintf("%s.%s.%s", Environment(), ProviderName(), providerConfigName)
}

func ShootK8sName(clusterName, clusterNamespace string) string {
	return fmt.Sprintf("s-%s", ctrlutils.NameHashSHAKE128Base32(clusterNamespace, clusterName))
}
func ShootK8sNameFromCluster(c *clustersv1alpha1.Cluster) string {
	if name, ok := c.Labels[providerv1alpha1.ShootNameLabel]; ok && name != "" {
		return name
	}
	return ShootK8sName(c.Name, c.Namespace)
}
