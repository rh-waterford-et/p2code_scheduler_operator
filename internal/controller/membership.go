package controller

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/labels"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	clusterv1beta2 "open-cluster-management.io/api/cluster/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *P2CodeSchedulingManifestReconciler) doesManagedClusterSetExist(managedClusterSetName string) (bool, error) {
	managedClusterSetList := &clusterv1beta2.ManagedClusterSetList{}
	if err := r.List(context.TODO(), managedClusterSetList); err != nil {
		return false, err
	}

	for _, managedClusterSet := range managedClusterSetList.Items {
		if managedClusterSet.Name == managedClusterSetName {
			return true, nil
		}
	}

	return false, nil
}

func (r *P2CodeSchedulingManifestReconciler) isManagedClusterInSet(managedClusterSetName string, managedClusterName string) (bool, error) {
	label := fmt.Sprintf("cluster.open-cluster-management.io/clusterset=%s", managedClusterSetName)
	labelSelector, err := labels.Parse(label)
	if err != nil {
		return false, fmt.Errorf("an occurred error creating the label selector")
	}

	listOptions := client.ListOptions{
		LabelSelector: labelSelector,
	}

	managedClusterList := &clusterv1.ManagedClusterList{}
	err = r.List(context.TODO(), managedClusterList, &listOptions)
	if err != nil {
		return false, fmt.Errorf("failed to list all managed clusters with the label %s", label)
	}

	for _, managedCluster := range managedClusterList.Items {
		if managedCluster.Name == managedClusterName {
			return true, nil
		}
	}

	return false, nil
}

func (r *P2CodeSchedulingManifestReconciler) isClusterSetBound(clustersetName string) (bool, error) {
	listOptions := client.ListOptions{
		Namespace: P2CodeSchedulerNamespace,
	}

	clusterSetBindingList := &clusterv1beta2.ManagedClusterSetBindingList{}
	if err := r.List(context.TODO(), clusterSetBindingList, &listOptions); err != nil {
		return false, err
	}

	for _, binding := range clusterSetBindingList.Items {
		if binding.Spec.ClusterSet == clustersetName {
			return true, nil
		}
	}

	return false, nil
}

func (r *P2CodeSchedulingManifestReconciler) isClusterSetEmpty(clustersetName string) (bool, error) {
	labelSelector := labels.SelectorFromSet(labels.Set{
		clusterv1beta2.ClusterSetLabel: clustersetName,
	})

	managedClusterList := &clusterv1.ManagedClusterList{}
	err := r.List(context.TODO(), managedClusterList, &client.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		return false, err
	}

	return len(managedClusterList.Items) < 1, nil
}
