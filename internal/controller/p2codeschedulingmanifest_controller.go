/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"slices"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	schedulingv1alpha1 "github.com/PoolPooer/p2code-scheduler/api/v1alpha1"
	ocmv1beta1 "open-cluster-management.io/api/cluster/v1beta1"
)

// P2CodeSchedulingManifestReconciler reconciles a P2CodeSchedulingManifest object
type P2CodeSchedulingManifestReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=scheduling.p2code.eu,resources=p2codeschedulingmanifests,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=scheduling.p2code.eu,resources=p2codeschedulingmanifests/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=scheduling.p2code.eu,resources=p2codeschedulingmanifests/finalizers,verbs=update
// +kubebuilder:rbac:groups=cluster.open-cluster-management.io,resources=placements,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cluster.open-cluster-management.io,resources=placementdecisions,verbs=get;list;watch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the P2CodeSchedulingManifest object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.19.0/pkg/reconcile
func (r *P2CodeSchedulingManifestReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	p2CodeSchedulingManifest := &schedulingv1alpha1.P2CodeSchedulingManifest{}
	err := r.Get(ctx, req.NamespacedName, p2CodeSchedulingManifest)

	if err != nil {
		log.Error(err, "Failed to get P2CodeSchedulingManifest")
		return ctrl.Result{}, err
	}

	log.Info("Contents of P2CodeSchedulingManifest", "manifest", p2CodeSchedulingManifest)

	placements, err := r.generatePlacementForManifests(p2CodeSchedulingManifest)
	if err != nil {
		log.Error(err, "Failed to generate Placements for manifests")
		return ctrl.Result{}, err
	}

	for _, placement := range placements {
		if err = r.Create(ctx, placement); err != nil {
			log.Error(err, "Failed to create Placement")
			return ctrl.Result{}, err
		}

		log.Info("Placement", "placement spec", placement.Spec)
	}

	placementDecisionList := &ocmv1beta1.PlacementDecisionList{}
	err = r.List(ctx, placementDecisionList)

	if err != nil {
		log.Error(err, "Cannot retrieve the list of PlacementDecisions")
		return ctrl.Result{}, err
	}

	for _, placementDecision := range placementDecisionList.Items {
		log.Info("Contents of PlacementDecision", "placementDecision", placementDecision)
	}

	return ctrl.Result{}, nil
}

func (r *P2CodeSchedulingManifestReconciler) generatePlacementForManifests(schedulingManifest *schedulingv1alpha1.P2CodeSchedulingManifest) ([]*ocmv1beta1.Placement, error) {
	var placementRules []metav1.LabelSelectorRequirement
	var numClusters int32 = 1
	placements := []*ocmv1beta1.Placement{}
	commonPlacementRules := extractPlacementRules(schedulingManifest.Spec.GlobalAnnotations)
	modifiedWorkloads := listModifiedWorkloads(schedulingManifest.Spec.WorkloadAnnotations)

	for _, manifest := range schedulingManifest.Spec.Manifests {
		object := &unstructured.Unstructured{}
		if err := object.UnmarshalJSON(manifest.Raw); err != nil {
			return nil, err
		}

		index := findWorkload(modifiedWorkloads, object.GetName())
		if index != -1 {
			additionalPlacementRules := extractPlacementRules(schedulingManifest.Spec.WorkloadAnnotations[index].Annotations)
			placementRules = slices.Concat(commonPlacementRules, additionalPlacementRules)
		} else {
			placementRules = commonPlacementRules
		}

		placement := &ocmv1beta1.Placement{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("%s-placement", object.GetName()),
				Namespace: schedulingManifest.Namespace,
			},
			Spec: ocmv1beta1.PlacementSpec{
				NumberOfClusters: &numClusters,
				ClusterSets: []string{
					"global",
				},
				Predicates: []ocmv1beta1.ClusterPredicate{
					{
						RequiredClusterSelector: ocmv1beta1.ClusterSelector{
							ClaimSelector: ocmv1beta1.ClusterClaimSelector{
								MatchExpressions: placementRules,
							},
						},
					},
				},
			},
		}

		placements = append(placements, placement)
	}

	return placements, nil
}

func findWorkload(workloads []string, targetWorkload string) int {
	for i, workload := range workloads {
		if workload == targetWorkload {
			return i
		}
	}
	return -1
}

func listModifiedWorkloads(workloadAnnotations []schedulingv1alpha1.WorkloadAnnotation) (modifiedWorkloads []string) {
	for _, workloadAnnotation := range workloadAnnotations {
		modifiedWorkloads = append(modifiedWorkloads, workloadAnnotation.Name)
	}
	return
}

// Assuming annotation being parsed is of the form p2code.filter.xx=yy
// Parse so that p2code.filter.xx is the key and yy is the value
func extractPlacementRules(annotations []string) []metav1.LabelSelectorRequirement {
	placementRules := []metav1.LabelSelectorRequirement{}
	for _, annotation := range annotations {
		splitAnnotation := strings.Split(annotation, "=")
		newPlacementRule := metav1.LabelSelectorRequirement{
			Key:      splitAnnotation[0],
			Operator: "In",
			Values: []string{
				splitAnnotation[1],
			},
		}
		placementRules = append(placementRules, newPlacementRule)
	}
	return placementRules
}

// SetupWithManager sets up the controller with the Manager.
func (r *P2CodeSchedulingManifestReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&schedulingv1alpha1.P2CodeSchedulingManifest{}).
		Complete(r)
}
