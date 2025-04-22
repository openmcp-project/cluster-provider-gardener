package shared

import (
	"fmt"
	"maps"
	"sync"

	"github.com/openmcp-project/controller-utils/pkg/clusters"

	"github.com/openmcp-project/cluster-provider-gardener/api/core/v1alpha1"
	providerv1alpha1 "github.com/openmcp-project/cluster-provider-gardener/api/core/v1alpha1"
)

// RuntimeConfiguration is a struct that holds the loaded ProviderConfigurations, enriched with further information gathered during runtime.
// For each instance of the ClusterProvider that is running, there should be one instance of this struct.
// It is important that it is shared between the ProviderConfiguration and the Cluster controllers. The former one will fill it with information that the latter one can use.
type RuntimeConfiguration struct {
	lock sync.RWMutex

	// providerConfigurations is a map of all providerConfigurations that are currently loaded, with their names as keys.
	providerConfigurations map[string]*providerv1alpha1.ProviderConfig
	// landscapes is a map of all Gardener landscapes mentioned in all ProviderConfigurations.
	// The landscape names are expected to be either unique or refer to the same landscape in case of the same name across all ProviderConfigurations.
	landscapes map[string]*Landscape
	// profiles is a map of all profiles derived from all ProviderConfigurations.
	// Their Landscape references are expected to reference the same object in case of the same landscape.
	// The first dimension is the name of the ProviderConfiguration that created this profile.
	// The second dimension is the name of the profile itself.
	profiles map[string]map[string]*Profile

	// not behind the lock because not modified after creation
	PlatformCluster   *clusters.Cluster
	OnboardingCluster *clusters.Cluster
}

func NewRuntimeConfiguration(platform, onboarding *clusters.Cluster) *RuntimeConfiguration {
	return &RuntimeConfiguration{
		PlatformCluster:   platform,
		OnboardingCluster: onboarding,
	}
}

func (rc *RuntimeConfiguration) GetProviderConfigurations() map[string]*providerv1alpha1.ProviderConfig {
	rc.lock.RLock()
	defer rc.lock.RUnlock()
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
	rc.lock.RLock()
	defer rc.lock.RUnlock()
	if rc.landscapes == nil {
		return nil
	}
	res := make(map[string]*Landscape, len(rc.landscapes))
	maps.Copy(res, rc.landscapes)
	return res
}

func (rc *RuntimeConfiguration) GetLandscape(name string) *Landscape {
	rc.lock.RLock()
	defer rc.lock.RUnlock()
	if rc.landscapes == nil {
		return nil
	}
	return rc.landscapes[name]
}

func (rc *RuntimeConfiguration) GetProfiles() map[string]map[string]*Profile {
	rc.lock.RLock()
	defer rc.lock.RUnlock()
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
	rc.lock.Lock()
	defer rc.lock.Unlock()
	rc.providerConfigurations = make(map[string]*providerv1alpha1.ProviderConfig, len(providerConfigurations))
	for k, v := range providerConfigurations {
		rc.providerConfigurations[k] = v.DeepCopy()
	}
}

func (rc *RuntimeConfiguration) SetLandscapes(landscapes map[string]*Landscape) {
	rc.lock.Lock()
	defer rc.lock.Unlock()
	rc.landscapes = make(map[string]*Landscape, len(landscapes))
	maps.Copy(rc.landscapes, landscapes)
}

func (rc *RuntimeConfiguration) SetLandscape(ls *Landscape) {
	rc.lock.Lock()
	defer rc.lock.Unlock()
	if rc.landscapes == nil {
		rc.landscapes = make(map[string]*Landscape)
	}
	rc.landscapes[ls.Name] = ls
}

func (rc *RuntimeConfiguration) UnsetLandscape(name string) {
	rc.lock.Lock()
	defer rc.lock.Unlock()
	if rc.landscapes == nil {
		return
	}
	delete(rc.landscapes, name)
}

func (rc *RuntimeConfiguration) SetProfiles(profiles map[string]map[string]*Profile) {
	rc.lock.Lock()
	defer rc.lock.Unlock()
	rc.profiles = make(map[string]map[string]*Profile, len(profiles))
	for k, v := range profiles {
		rc.profiles[k] = make(map[string]*Profile, len(v))
		for k2, v2 := range v {
			rc.profiles[k][k2] = v2.DeepCopy()
		}
	}
}

func (rc *RuntimeConfiguration) SetProfilesForProviderConfiguration(providerConfigurationName string, profiles []*Profile) {
	rc.lock.Lock()
	defer rc.lock.Unlock()
	if rc.profiles == nil {
		rc.profiles = make(map[string]map[string]*Profile)
	}
	rc.profiles[providerConfigurationName] = make(map[string]*Profile, len(profiles))
	for _, p := range profiles {
		rc.profiles[providerConfigurationName][p.GetName()] = p.DeepCopy()
	}
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

type CompletedProfile struct {
	RuntimeData
	Raw       Profile
	Config    *v1alpha1.ProviderConfig
	Landscape *Landscape
}

type Profile struct {
	Config    string
	Landscape string
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
		Config:    p.Config,
		Landscape: p.Landscape,
	}
}

func (p *Profile) GetName() string {
	return fmt.Sprintf("%s/%s", p.Landscape, p.Config)
}
