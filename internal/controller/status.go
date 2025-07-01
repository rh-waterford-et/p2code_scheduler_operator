package controller

import (
	"context"

	schedulingv1alpha1 "github.com/PoolPooer/p2code-scheduler/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func (r *P2CodeSchedulingManifestReconciler) UpdateStatus(p2CodeSchedulingManifest *schedulingv1alpha1.P2CodeSchedulingManifest, condition metav1.Condition, schedulingDecisions []schedulingv1alpha1.SchedulingDecision) error {
	// Refetch P2CodeSchedulingManifest instance before updating the resource
	if err := r.Get(context.TODO(), types.NamespacedName{Name: p2CodeSchedulingManifest.Name, Namespace: p2CodeSchedulingManifest.Namespace}, p2CodeSchedulingManifest); err != nil {
		return err
	}

	meta.SetStatusCondition(&p2CodeSchedulingManifest.Status.Conditions, condition)
	p2CodeSchedulingManifest.Status.Decisions = schedulingDecisions
	if err := r.Status().Update(context.TODO(), p2CodeSchedulingManifest); err != nil {
		return err
	}

	return nil
}
