package controller

import (
	"fmt"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type BasicResourceInfo struct {
	name string
	kind string
}

type ManifestMetadata struct {
	name             string
	namespace        string
	groupVersionKind schema.GroupVersionKind
	labels           map[string]string
}

type Resource struct {
	metadata                    ManifestMetadata
	p2codeSchedulingAnnotations []string
	manifest                    runtime.RawExtension
}

type SupportedAPIGroup struct {
	apiVersion       string
	allowedResources []string
	blockedResources []string
}

type ResourceSet []*Resource
type AbsentResourceSet []*BasicResourceInfo

func (r Resource) IsWorkload() bool {
	group := r.metadata.groupVersionKind.Group
	kind := r.metadata.groupVersionKind.Kind

	// Workload resources are in the apps API group apart
	if group == "apps" {
		return true
	}

	// A Pod is a workload resource in the core API group
	if group == "" && kind == "Pod" {
		return true
	}

	// Jobs and CronJobs in the batch API group are considered a workload resource
	if group == "batch" {
		return true
	}

	return false
}

func (r1 Resource) Equals(r2 Resource) bool {
	return r1.metadata.name == r2.metadata.name && r1.metadata.namespace == r2.metadata.namespace && r1.metadata.groupVersionKind.Kind == r2.metadata.groupVersionKind.Kind
}

func (resourceSet *ResourceSet) Add(r *Resource) {
	for _, resource := range *resourceSet {
		if resource.Equals(*r) {
			return
		}
	}

	*resourceSet = append(*resourceSet, r)
}

func (resourceSet *ResourceSet) Merge(rs *ResourceSet) {
	for _, resource := range *rs {
		resourceSet.Add(resource)
	}
}

func (resourceSet *ResourceSet) Find(name string, kind string) (*Resource, error) {
	for _, resource := range resourceSet.FilterByKind(kind) {
		if resource.metadata.name == name {
			return resource, nil
		}
	}

	errorMessage := fmt.Sprintf("cannot find a resource of type %s with the name %s", kind, name)
	return nil, &ResourceNotFoundError{errorMessage}
}

func (resourceSet *ResourceSet) FindWorkload(name string) (*Resource, error) {
	for _, resource := range *resourceSet {
		if resource.metadata.name == name && resource.IsWorkload() {
			return resource, nil
		}
	}

	errorMessage := fmt.Sprintf("cannot find a workload resource with the name %s", name)
	return nil, &ResourceNotFoundError{errorMessage}
}

func (resourceSet *ResourceSet) FilterByKind(kind string) ResourceSet {
	list := ResourceSet{}
	for _, resource := range *resourceSet {
		if resource.metadata.groupVersionKind.Kind == kind {
			list = append(list, resource)
		}
	}

	return list
}

func (resourceSet *ResourceSet) Categorise() (workloads ResourceSet, ancillaryResources ResourceSet) {
	for _, resource := range *resourceSet {
		if resource.IsWorkload() {
			workloads = append(workloads, resource)
		} else {
			// Add additional checks here - isCoreAncillary resource as fn name ???
			ancillaryResources = append(ancillaryResources, resource)
		}
	}

	return
}

// Convert the manifest to a Resource for easier manipulation for objects that can be handled by the scheduler
// The scheduler is responsible for the management of k8s workloads and any k8s workload ancillary resources
// All other resources are considered cluster admin resources and beyond the scope of the scheduler
func bulkConvertToResourceSet(manifests []runtime.RawExtension) (ResourceSet, error) {
	resources := ResourceSet{}
	for _, manifest := range manifests {
		object := &unstructured.Unstructured{}
		if err := object.UnmarshalJSON(manifest.Raw); err != nil {
			return ResourceSet{}, fmt.Errorf("%w", err)
		}

		metadata := ManifestMetadata{name: object.GetName(), namespace: object.GetNamespace(), groupVersionKind: object.GetObjectKind().GroupVersionKind(), labels: object.GetLabels()}
		// Analyse the manifest metadata to determine whether the scheduler should support this resource or not
		if err := metadata.isValid(); err != nil {
			return ResourceSet{}, err
		} else {
			resources.Add(&Resource{metadata: metadata, manifest: manifest})
		}
	}
	return resources, nil
}

func (m ManifestMetadata) isValid() error {
	if !m.isSupportedResource() {
		errorMessage := fmt.Sprintf("unsupported manifest: the cluster admin is responsible for managing %s resources", strings.ToLower(m.groupVersionKind.Kind))
		return &MisconfiguredManifestError{errorMessage}
	}

	if !m.hasNamespace() {
		errorMessage := fmt.Sprintf("invalid manifest: missing namespace for %s with the name %s", strings.ToLower(m.groupVersionKind.Kind), m.name)
		return &MisconfiguredManifestError{errorMessage}
	}

	return nil
}

func (m ManifestMetadata) hasNamespace() bool {
	// Ensure a namespace is defined for the resource unless the resource is not namespaced
	nonNamespacedResources := []string{"Namespace", "ClusterRole", "ClusterRoleBinding"}
	if m.namespace == "" && slices.Contains(nonNamespacedResources, m.groupVersionKind.Kind) {
		return true
	}

	return m.namespace != ""
}

func (m ManifestMetadata) isSupportedResource() bool {
	coreBlockedResources := []string{"PersistentVolume"}
	monitoringAllowedResources := []string{"ServiceMonitor"}

	supportedAPIGroups := []SupportedAPIGroup{
		{apiVersion: "v1", blockedResources: coreBlockedResources},
		{apiVersion: "apps/v1"},
		{apiVersion: "batch/v1"},
		{apiVersion: "rbac.authorization.k8s.io/v1"},
		{apiVersion: "authorization.openshift.io/v1"},
		{apiVersion: "route.openshift.io/v1"},
		{apiVersion: "networking.k8s.io/v1"},
		{apiVersion: "autoscaling/v2"},
		{apiVersion: "monitoring.coreos.com/v1", allowedResources: monitoringAllowedResources},
	}

	gvk := m.groupVersionKind
	manifestAPIVersion := gvk.GroupVersion().String()

	for _, supportedAPIGroup := range supportedAPIGroups {
		if supportedAPIGroup.apiVersion == manifestAPIVersion && supportedAPIGroup.isResourceAllowed(gvk) {
			return true
		}
	}

	return false
}

func (supportedAPIGroup SupportedAPIGroup) isResourceAllowed(gvk schema.GroupVersionKind) bool {
	// If no allowed or blocked resources explicitly defined assume all resources in that API group are allowed
	if len(supportedAPIGroup.allowedResources) == 0 && len(supportedAPIGroup.blockedResources) == 0 {
		return true
	}

	if len(supportedAPIGroup.allowedResources) > 0 && len(supportedAPIGroup.blockedResources) > 0 {
		return slices.Contains(supportedAPIGroup.allowedResources, gvk.Kind) || !slices.Contains(supportedAPIGroup.blockedResources, gvk.Kind)
	}

	if len(supportedAPIGroup.allowedResources) > 0 && len(supportedAPIGroup.blockedResources) == 0 {
		return slices.Contains(supportedAPIGroup.allowedResources, gvk.Kind)
	}

	if len(supportedAPIGroup.allowedResources) == 0 && len(supportedAPIGroup.blockedResources) > 0 {
		return !slices.Contains(supportedAPIGroup.blockedResources, gvk.Kind)
	}

	return false
}

func (absentResourceSet *AbsentResourceSet) Register(resourceName string, resourceKind string) {
	*absentResourceSet = append(*absentResourceSet, &BasicResourceInfo{name: resourceName, kind: resourceKind})
}

func (absentResourceSet *AbsentResourceSet) Merge(rs *AbsentResourceSet) {
	*absentResourceSet = append(*absentResourceSet, *rs...)
}
