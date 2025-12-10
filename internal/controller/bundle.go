package controller

import (
	"fmt"
	"slices"

	schedulingv1alpha1 "github.com/rh-waterford-et/p2code-scheduler-operator/api/v1alpha1"
	"github.com/rh-waterford-et/p2code-scheduler-operator/utils"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type Bundle struct {
	name                string
	resources           ResourceSet
	absentResources     AbsentResourceSet
	externalConnections []ServicePortPair
	schedulingDetails   SchedulingDetails
}

type SchedulingDetails struct {
	placementRequests []metav1.LabelSelectorRequirement
	clusterName       string
	// The name to use for any scheduling related resources eg placement and manifest works that are associated with the bundle
	resourceName string
}

type BundleList []*Bundle

func (b *Bundle) update(placementRequests []metav1.LabelSelectorRequirement, resources ResourceSet) {
	b.resources = resources
	b.absentResources = AbsentResourceSet{}
	b.externalConnections = []ServicePortPair{}
	b.schedulingDetails = SchedulingDetails{}
}

func (bl BundleList) addBundle(bundle *Bundle) {
	bl = append(bl, bundle)
}

func (bl BundleList) getBundle(bundleName string) *Bundle {
	for _, bundle := range bl {
		if bundle.name == bundleName {
			return bundle
		}
	}
	return nil
}

func (bl BundleList) listBundles() []string {
	bundleNames := []string{}
	for _, bundle := range bl {
		bundleNames = append(bundleNames, bundle.name)
	}

	return bundleNames
}

func (r *P2CodeSchedulingManifestReconciler) deleteBundles(ownerReference string) {
	delete(r.Bundles, ownerReference)
}

// nolint:cyclop // not to concenred about cognitive complexity (brainfreeze)
func buildAndUpdateBundles(bundles BundleList, p2CodeSchedulingManifest *schedulingv1alpha1.P2CodeSchedulingManifest) (BundleList, error) {
	// Convert p2CodeSchedulingManifest.Spec.Manifests to Resources for easier manipulation
	resources, err := bulkConvertToResourceSet(p2CodeSchedulingManifest.Spec.Manifests)
	if err != nil {
		return BundleList{}, err
	}

	commonPlacementRules := ExtractPlacementRules(p2CodeSchedulingManifest.Spec.GlobalAnnotations)

	// If no filter annotations are specified all manifests are scheduled to a random cluster within the target managed cluster set
	// Create an empty placement and allow the Placement API to decide on a random cluster from the target managed cluster set
	// Later take into account the resource requests of each workload
	if len(commonPlacementRules) == 0 && len(p2CodeSchedulingManifest.Spec.WorkloadAnnotations) == 0 {
		// Check if a default bundle already exists
		bundleName := "default"
		b := bundles.getBundle(bundleName)
		if b != nil {
			b.update([]metav1.LabelSelectorRequirement{}, resources)
		} else {
			bundle := &Bundle{name: bundleName, schedulingDetails: SchedulingDetails{placementRequests: []metav1.LabelSelectorRequirement{}, resourceName: utils.TruncateNameIfNeeded(fmt.Sprintf("%s-%s", bundleName, p2CodeSchedulingManifest.Name))}, resources: resources}
			bundles.addBundle(bundle)
		}
	}

	// If no filter annotations are specified at the workload level but filter annotations are provided at the global level all manifests are scheduled to a cluster matching the global filter annotations
	if len(commonPlacementRules) > 0 && len(p2CodeSchedulingManifest.Spec.WorkloadAnnotations) == 0 {
		// Check if a global bundle already exists
		bundleName := "global"
		b := bundles.getBundle(bundleName)
		if b != nil {
			b.update(commonPlacementRules, resources)
		} else {
			bundle := &Bundle{name: bundleName, schedulingDetails: SchedulingDetails{placementRequests: commonPlacementRules, resourceName: utils.TruncateNameIfNeeded(fmt.Sprintf("%s-%s", bundleName, p2CodeSchedulingManifest.Name))}, resources: resources}
			bundles.addBundle(bundle)
		}
	}

	// Check if any workload filter annotations are specified
	// Bundle workload and its ancillary resources with the filter annotation
	if len(p2CodeSchedulingManifest.Spec.WorkloadAnnotations) > 0 {
		workloads, ancillaryResources := resources.Categorise()

		// Ensure workloadAnnotations refer to a valid workload
		if err := AssignWorkloadAnnotations(workloads, p2CodeSchedulingManifest.Spec.WorkloadAnnotations); err != nil {
			errorMessage := fmt.Sprintf("invalid workload annotation provided : %s", err.Error())
			return BundleList{}, &MisconfiguredManifestError{errorMessage}
		}

		for _, workload := range workloads {
			// TODO calculate workload ResourceRequests
			workloadAncillaryResources, missingDependentResources, externalConnections, err := analyseWorkload(workload, ancillaryResources)
			if err != nil {
				return BundleList{}, fmt.Errorf("%w", err)
			}

			bundleResources := ResourceSet{}
			bundleResources = append(bundleResources, workload)
			bundleResources = append(bundleResources, workloadAncillaryResources...)

			additionalPlacementRules := ExtractPlacementRules(workload.p2codeSchedulingAnnotations)
			placementRules := slices.Concat(commonPlacementRules, additionalPlacementRules)

			// Check if a bundle already exists for the workload
			// nolint // used in the if != err section
			bundleName := workload.metadata.name
			b := bundles.getBundle(bundleName)

			if b != nil {
				b.schedulingDetails.placementRequests = placementRules
				b.resources = bundleResources
				b.absentResources = missingDependentResources
				b.externalConnections = externalConnections
			} else {
				bundle := &Bundle{name: bundleName, schedulingDetails: SchedulingDetails{placementRequests: placementRules, resourceName: utils.TruncateNameIfNeeded(fmt.Sprintf("%s-%s", bundleName, p2CodeSchedulingManifest.Name))}, resources: bundleResources, absentResources: missingDependentResources, externalConnections: externalConnections}
				bundles.addBundle(bundle)
			}

		}
	}

	return bundles, nil
}
