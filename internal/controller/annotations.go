package controller

import (
	"fmt"
	"strings"

	schedulingv1alpha1 "github.com/rh-waterford-et/p2code-scheduler-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Annotations and annotation prefixes supported by the scheduler
const (
	filterPrefix                  = "p2code.filter"
	targetClusterSetAnnotationKey = "p2code.target.managedClusterSet"
	targetClusterAnnotationKey    = "p2code.target.cluster"
)

func IsAnnotationSupported(s string) bool {
	key := strings.Split(s, "=")[0]
	return key == targetClusterAnnotationKey || key == targetClusterSetAnnotationKey || strings.HasPrefix(key, filterPrefix)
}

func ValidateAnnotationsSupported(p2codeSchedulingManifest *schedulingv1alpha1.P2CodeSchedulingManifest) (bool, error) {
	for _, annotation := range p2codeSchedulingManifest.Spec.GlobalAnnotations {
		if !IsAnnotationSupported(annotation) {
			return false, fmt.Errorf("%s is an unsupported annotation", annotation)
		}
	}

	for _, workloadAnnotation := range p2codeSchedulingManifest.Spec.WorkloadAnnotations {
		for _, annotation := range workloadAnnotation.Annotations {
			if !IsAnnotationSupported(annotation) {
				return false, fmt.Errorf("%s is an unsupported annotation", annotation)
			}
		}
	}

	return true, nil
}

func AssignWorkloadAnnotations(workloads ResourceSet, workloadAnnotations []schedulingv1alpha1.WorkloadAnnotation) error {
	for index, workloadAnnotation := range workloadAnnotations {
		// Ensure that all the workload annotations are filter annotations
		// Use the extractPlacementRules function to get a list of the filter annotations
		if len(ExtractPlacementRules(workloadAnnotation.Annotations)) != len(workloadAnnotation.Annotations) {
			return fmt.Errorf("invalid workload annotations provided, all workload annotations must be of the form p2code.filter.feature=value")
		}

		workload, err := workloads.FindWorkload(workloadAnnotation.Name)
		if err != nil {
			return fmt.Errorf("invalid workload name for workload annotation %d: %w", index+1, err)
		}

		workload.p2codeSchedulingAnnotations = workloadAnnotation.Annotations
	}
	return nil
}

// Placement annotations are expected to follow the format p2code.filter.x=y
// Parse the annotation so that p2code.filter.x is the key and y is the value
func ExtractPlacementRules(annotations []string) []metav1.LabelSelectorRequirement {
	placementRules := []metav1.LabelSelectorRequirement{}
	for _, annotation := range annotations {
		splitAnnotation := strings.Split(annotation, "=")
		// Check that the annnotation is a filter annotation as other forms of annotations are allowed
		if strings.HasPrefix(splitAnnotation[0], filterPrefix) {
			newPlacementRule := metav1.LabelSelectorRequirement{
				Key:      splitAnnotation[0],
				Operator: "In",
				Values: []string{
					splitAnnotation[1],
				},
			}
			placementRules = append(placementRules, newPlacementRule)
		}
	}
	return placementRules
}

// At least one target annotation is expected
// There must be a target annotation specifying the managed cluster set to use, the annotation should be of the form p2code.target.managedClusterSet=x where x is the name of the managed cluster set
// The target annotation p2code.target.cluster=y where y is the name of the cluster is an optional annotation specifying the exact cluster that the workloads should be scheduled to
func ExtractTarget(annotations []string) (targetClusterSet string, targetCluster string, err error) {
	for _, annotation := range annotations {
		splitAnnotation := strings.Split(annotation, "=")
		if splitAnnotation[0] == targetClusterSetAnnotationKey {
			targetClusterSet = splitAnnotation[1]
		} else if splitAnnotation[0] == targetClusterAnnotationKey {
			targetCluster = splitAnnotation[1]
		}
	}

	if targetClusterSet == "" {
		err = fmt.Errorf("no target managed cluster set provided")
	}

	return
}
