package shared

import (
	"fmt"
	"maps"
	"sync"

	"github.com/openmcp-project/controller-utils/pkg/clusters"

	clustersv1alpha1 "github.com/openmcp-project/cluster-provider-gardener/api/clusters/v1alpha1"
	providerv1alpha1 "github.com/openmcp-project/cluster-provider-gardener/api/core/v1alpha1"
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
	// The first dimension is the name of the ProviderConfiguration that created this profile.
	// The second dimension is the name of the profile itself.
	profiles map[string]map[string]*Profile

	// not behind the lock because not modified after creation
	PlatformCluster   *clusters.Cluster
	OnboardingCluster *clusters.Cluster
}

func NewRuntimeConfiguration(platform, onboarding *clusters.Cluster) *RuntimeConfiguration {
	return &RuntimeConfiguration{
		Lock:              &sync.RWMutex{},
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

func (rc *RuntimeConfiguration) GetLandscapes() map[string]*Landscape {
	if rc.landscapes == nil {
		return nil
	}
	res := make(map[string]*Landscape, len(rc.landscapes))
	maps.Copy(res, rc.landscapes)
	return res
}

func (rc *RuntimeConfiguration) GetLandscape(name string) *Landscape {
	if rc.landscapes == nil {
		return nil
	}
	return rc.landscapes[name]
}

func (rc *RuntimeConfiguration) GetProfiles() map[string]map[string]*Profile {
	if rc.profiles == nil {
		return nil
	}
	res := make(map[string]map[string]*Profile, len(rc.profiles))
	for k, v := range rc.profiles {
		res[k] = make(map[string]*Profile, len(v))
		for k2, v2 := range v {
			res[k][k2] = v2.DeepCopy()
		}
	}
	return res
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

func (rc *RuntimeConfiguration) SetLandscapes(landscapes map[string]*Landscape) {
	rc.landscapes = make(map[string]*Landscape, len(landscapes))
	maps.Copy(rc.landscapes, landscapes)
}

func (rc *RuntimeConfiguration) SetLandscape(ls *Landscape) {
	if rc.landscapes == nil {
		rc.landscapes = make(map[string]*Landscape)
	}
	rc.landscapes[ls.Name] = ls
}

func (rc *RuntimeConfiguration) UnsetLandscape(name string) {
	if rc.landscapes == nil {
		return
	}
	delete(rc.landscapes, name)
}

func (rc *RuntimeConfiguration) SetProfiles(profiles map[string]map[string]*Profile) {
	rc.profiles = make(map[string]map[string]*Profile, len(profiles))
	for k, v := range profiles {
		rc.profiles[k] = make(map[string]*Profile, len(v))
		for k2, v2 := range v {
			rc.profiles[k][k2] = v2.DeepCopy()
		}
	}
}

func (rc *RuntimeConfiguration) SetProfilesForProviderConfiguration(providerConfigurationName string, profiles map[string]*Profile) {
	if rc.profiles == nil {
		rc.profiles = make(map[string]map[string]*Profile)
	}
	rc.profiles[providerConfigurationName] = make(map[string]*Profile, len(profiles))
	maps.Copy(rc.profiles[providerConfigurationName], profiles)
}

func (rc *RuntimeConfiguration) UnsetProfilesForProviderConfiguration(providerConfigurationName string) {
	if rc.profiles == nil {
		return
	}
	delete(rc.profiles, providerConfigurationName)
}

type Landscape struct {
	Name     string
	Cluster  *clusters.Cluster
	Resource *providerv1alpha1.Landscape
}

func (l *Landscape) Available() bool {
	return l != nil && l.Resource != nil && l.Resource.Status.Phase == providerv1alpha1.LANDSCAPE_PHASE_AVAILABLE
}

func (l *Landscape) Projects() []string {
	if l == nil || l.Resource == nil {
		return nil
	}
	projects := make([]string, len(l.Resource.Status.Projects))
	copy(projects, l.Resource.Status.Projects)
	return projects
}

func (l *Landscape) DeepCopy() *Landscape {
	return &Landscape{
		Name:     l.Name,
		Cluster:  l.Cluster,
		Resource: l.Resource.DeepCopy(),
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
	}
}

func (p *Profile) DeepCopy() *Profile {
	return &Profile{
		Config:               p.Config.DeepCopy(),
		ProviderConfigSource: p.ProviderConfigSource,
	}
}

func (p *Profile) GetName() string {
	return fmt.Sprintf("%s/%s", p.Config.LandscapeRef.Name, p.Config.Name)
}
