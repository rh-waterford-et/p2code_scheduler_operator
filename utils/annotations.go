package utils

import (
	"fmt"
	"strings"

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
