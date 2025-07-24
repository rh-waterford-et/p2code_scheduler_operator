package controller

import (
	"context"
	"fmt"

	schedulingv1alpha1 "github.com/rh-waterford-et/p2code-scheduler-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func (r *P2CodeSchedulingManifestReconciler) UpdateStatus(ctx context.Context, p2CodeSchedulingManifest *schedulingv1alpha1.P2CodeSchedulingManifest, condition metav1.Condition, schedulingDecisions []schedulingv1alpha1.SchedulingDecision) error {
	// Refetch P2CodeSchedulingManifest instance before updating the resource
	if err := r.Get(ctx, types.NamespacedName{Name: p2CodeSchedulingManifest.Name, Namespace: p2CodeSchedulingManifest.Namespace}, p2CodeSchedulingManifest); err != nil {
		return fmt.Errorf("%w", err)
	}

	meta.SetStatusCondition(&p2CodeSchedulingManifest.Status.Conditions, condition)
	p2CodeSchedulingManifest.Status.Decisions = schedulingDecisions
	if err := r.Status().Update(ctx, p2CodeSchedulingManifest); err != nil {
		return fmt.Errorf("%w", err)
	}

	return nil
}
