package controller

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

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

type ResourceSet []*Resource

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
			ancillaryResources = append(ancillaryResources, resource)
		}
	}

	return
}

func bulkConvertToResourceSet(manifests []runtime.RawExtension) (ResourceSet, error) {
	resources := ResourceSet{}
	for _, manifest := range manifests {
		object := &unstructured.Unstructured{}
		if err := object.UnmarshalJSON(manifest.Raw); err != nil {
			return ResourceSet{}, fmt.Errorf("%w", err)
		}

		metadata := ManifestMetadata{name: object.GetName(), namespace: object.GetNamespace(), groupVersionKind: object.GetObjectKind().GroupVersionKind(), labels: object.GetLabels()}
		resources.Add(&Resource{metadata: metadata, manifest: manifest})
	}
	return resources, nil
}
