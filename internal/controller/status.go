package controller

import (
	"context"
	"fmt"

	schedulingv1alpha1 "github.com/rh-waterford-et/p2code-scheduler-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Status conditions corresponding to the state of the P2CodeSchedulingManifest
const (
	schedulingInProgress = "SchedulingInProgress"
	schedulingSuccessful = "SchedulingSuccessful"
	unreliablyScheduled  = "ScheduledWithUnreliableConnectivity"
	schedulingFailed     = "SchedulingFailed"
	tentativelyScheduled = "TentativelyScheduled"
	finalizing           = "Finalizing"
	misconfigured        = "Misconfigured"
)

func (r *P2CodeSchedulingManifestReconciler) UpdateStatus(ctx context.Context, p2CodeSchedulingManifest *schedulingv1alpha1.P2CodeSchedulingManifest, condition metav1.Condition, schedulingDecisions []schedulingv1alpha1.SchedulingDecision) error {
	// Refetch P2CodeSchedulingManifest instance before updating the resource
	if err := r.Get(ctx, types.NamespacedName{Name: p2CodeSchedulingManifest.Name, Namespace: p2CodeSchedulingManifest.Namespace}, p2CodeSchedulingManifest); err != nil {
		return fmt.Errorf("%w", err)
	}

	meta.SetStatusCondition(&p2CodeSchedulingManifest.Status.Conditions, condition)
	p2CodeSchedulingManifest.Status.Decisions = schedulingDecisions
	p2CodeSchedulingManifest.Status.State = condition.Type
	if err := r.Status().Update(ctx, p2CodeSchedulingManifest); err != nil {
		return fmt.Errorf("%w", err)
	}

	return nil
}

func getInProgressState(ctx context.Context, p2CodeSchedulingManifest *schedulingv1alpha1.P2CodeSchedulingManifest) *metav1.Condition {
	// If the status is empty set the status as scheduling in progress
	if len(p2CodeSchedulingManifest.Status.Conditions) == 0 {
		condition := metav1.Condition{Type: schedulingInProgress, Status: metav1.ConditionTrue, Reason: schedulingInProgress, Message: "Analysing scheduling requirements"}
		return &condition
	} else if p2CodeSchedulingManifest.GetDeletionTimestamp() == nil && p2CodeSchedulingManifest.Status.State != schedulingInProgress {
		// Update the status to scheduling in progress if changes have been made to the P2CodeSchedulingManifest causing the manifests to be rescheduled
		condition := metav1.Condition{Type: schedulingInProgress, Status: metav1.ConditionTrue, Reason: schedulingInProgress, Message: "Reanalysing scheduling requirements"}
		return &condition
	}

	return nil
}
