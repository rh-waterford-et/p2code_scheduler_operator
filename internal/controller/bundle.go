package controller

import (
	"fmt"
	"slices"

	schedulingv1alpha1 "github.com/rh-waterford-et/p2code-scheduler-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type Bundle struct {
	name                string
	placementRequests   []metav1.LabelSelectorRequirement
	resources           ResourceSet
	externalConnections []ServicePortPair
	clusterName         string
}

func (r *P2CodeSchedulingManifestReconciler) addBundle(bundle *Bundle, ownerReference string) {
	r.Bundles[ownerReference] = append(r.Bundles[ownerReference], bundle)
}

func (r *P2CodeSchedulingManifestReconciler) getBundle(bundleName string, ownerReference string) (*Bundle, error) {
	for _, bundle := range r.Bundles[ownerReference] {
		if bundle.name == bundleName {
			return bundle, nil
		}
	}
	return nil, fmt.Errorf("cannot find a bundle with the name %s", bundleName)
}

func (r *P2CodeSchedulingManifestReconciler) deleteBundles(ownerReference string) {
	delete(r.Bundles, ownerReference)
}

// nolint:cyclop // not to concenred about cognitive complexity (brainfreeze)
func (r *P2CodeSchedulingManifestReconciler) buildBundle(p2CodeSchedulingManifest *schedulingv1alpha1.P2CodeSchedulingManifest) error {
	// Convert p2CodeSchedulingManifest.Spec.Manifests to Resources for easier manipulation
	resources, err := bulkConvertToResourceSet(p2CodeSchedulingManifest.Spec.Manifests)
	if err != nil {
		errorMessage := fmt.Errorf("failed to process manifests to be scheduled: %w", err)
		return errorMessage
	}

	commonPlacementRules := ExtractPlacementRules(p2CodeSchedulingManifest.Spec.GlobalAnnotations)

	// If no filter annotations are specified all manifests are scheduled to a random cluster within the target managed cluster set
	// Create an empty placement and allow the Placement API to decide on a random cluster from the target managed cluster set
	// Later take into account the resource requests of each workload
	if len(commonPlacementRules) == 0 && len(p2CodeSchedulingManifest.Spec.WorkloadAnnotations) == 0 {
		// Check if a default bundle already exists
		_, err := r.getBundle("default", p2CodeSchedulingManifest.Name)
		if err != nil {
			bundle := &Bundle{name: "default", placementRequests: []metav1.LabelSelectorRequirement{}, resources: resources}
			r.addBundle(bundle, p2CodeSchedulingManifest.Name)
		}
	}

	// If no filter annotations are specified at the workload level but filter annotations are provided at the global level all manifests are scheduled to a cluster matching the global filter annotations
	if len(commonPlacementRules) > 0 && len(p2CodeSchedulingManifest.Spec.WorkloadAnnotations) == 0 {
		// Check if a global bundle already exists
		_, err := r.getBundle("global", p2CodeSchedulingManifest.Name)
		if err != nil {
			bundle := &Bundle{name: "global", placementRequests: commonPlacementRules, resources: resources}
			r.addBundle(bundle, p2CodeSchedulingManifest.Name)
		}
	}

	// Check if any workload filter annotations are specified
	// Bundle workload and its ancillary resources with the filter annotation
	if len(p2CodeSchedulingManifest.Spec.WorkloadAnnotations) > 0 {
		workloads, ancillaryResources := resources.Categorise()

		// Ensure workloadAnnotations refer to a valid workload
		if err := AssignWorkloadAnnotations(workloads, p2CodeSchedulingManifest.Spec.WorkloadAnnotations); err != nil {
			errorMessage := fmt.Sprintf("invalid workload annotation provided : %s", err.Error())
			return &MisconfiguredManifestError{errorMessage}
		}

		for _, workload := range workloads {
			// Check if a bundle already exists for the workload
			// Create a bundle for the workload if needed and find its ancillary resources
			// nolint // used in the if != err section
			bundle, err := r.getBundle(workload.metadata.name, p2CodeSchedulingManifest.Name)
			if err != nil {
				// TODO calculate workload ResourceRequests
				workloadAncillaryResources, externalConnections, err := analyseWorkload(workload, ancillaryResources)
				if err != nil {
					return fmt.Errorf("%w", err)
				}

				bundleResources := ResourceSet{}
				bundleResources = append(bundleResources, workload)
				bundleResources = append(bundleResources, workloadAncillaryResources...)

				additionalPlacementRules := ExtractPlacementRules(workload.p2codeSchedulingAnnotations)
				placementRules := slices.Concat(commonPlacementRules, additionalPlacementRules)

				bundle = &Bundle{name: workload.metadata.name, placementRequests: placementRules, resources: bundleResources, externalConnections: externalConnections}
				r.addBundle(bundle, p2CodeSchedulingManifest.Name)
			}
		}
	}

	return nil
}
