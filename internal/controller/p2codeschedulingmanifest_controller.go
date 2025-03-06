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
	"time"

	"github.com/iancoleman/strcase"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	schedulingv1alpha1 "github.com/PoolPooer/p2code-scheduler/api/v1alpha1"
	clusterv1beta1 "open-cluster-management.io/api/cluster/v1beta1"
	workv1 "open-cluster-management.io/api/work/v1"
)

const finalizer = "scheduling.p2code.eu/finalizer"
const ownershipLabel = "scheduling.p2code.eu/owner"
const P2CodeSchedulerNamespace = "p2code-scheduler-system"

// Status conditions corresponding to the state of the P2CodeSchedulingManifest
const (
	typeSchedulingSucceeded = "SchedulingSucceeded"
	typeSchedulingFailed    = "SchedulingFailed"
	typeFinalizing          = "Finalizing"
)

// P2CodeSchedulingManifestReconciler reconciles a P2CodeSchedulingManifest object
type P2CodeSchedulingManifestReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	// Var for manifest works or bundles
}

// Might to consider namespace too
type Resource struct {
	placed   bool
	kind     string
	name     string
	manifest runtime.RawExtension
}

type ResourceCollection struct {
	workloads          []Resource
	ancillaryResources []Resource
}

type Bundle struct {
	mainWorkload                Resource
	ancillaryResourcesManifests runtime.RawExtension
	placementDecision           string
}

// +kubebuilder:rbac:groups=scheduling.p2code.eu,resources=p2codeschedulingmanifests,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=scheduling.p2code.eu,resources=p2codeschedulingmanifests/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=scheduling.p2code.eu,resources=p2codeschedulingmanifests/finalizers,verbs=update
// +kubebuilder:rbac:groups=cluster.open-cluster-management.io,resources=placements,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cluster.open-cluster-management.io,resources=placementdecisions,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups=work.open-cluster-management.io,resources=manifestworks,verbs=get;list;watch;create;update;patch;delete

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

	// Get P2CodeSchedulingManifest instance
	p2CodeSchedulingManifest := &schedulingv1alpha1.P2CodeSchedulingManifest{}
	err := r.Get(ctx, req.NamespacedName, p2CodeSchedulingManifest)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Stop reconciliation if the resource is not found or has been deleted
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get P2CodeSchedulingManifest")
		return ctrl.Result{}, err
	}

	// Validate that the P2CodeSchedulingManifest instance is in the correct namespace
	if req.Namespace != P2CodeSchedulerNamespace {
		errorTitle := "incorrect namespace"
		errorMessage := "ignoring resource as it is not in the p2code-scheduler-system namespace"

		err := fmt.Errorf("%s", errorTitle)
		log.Error(err, errorMessage)

		meta.SetStatusCondition(&p2CodeSchedulingManifest.Status.Conditions, metav1.Condition{Type: typeSchedulingFailed, Status: metav1.ConditionTrue, Reason: strcase.ToCamel(errorTitle), Message: errorMessage})
		p2CodeSchedulingManifest.Status.Decisions = []schedulingv1alpha1.SchedulingDecision{}
		if err := r.Status().Update(ctx, p2CodeSchedulingManifest); err != nil {
			log.Error(err, "Failed to update P2CodeSchedulingManifest status")
			return ctrl.Result{}, err
		}

		return ctrl.Result{}, err
	}

	// Check if P2CodeSchedulingManifest instance is marked for deletion
	if p2CodeSchedulingManifest.GetDeletionTimestamp() != nil {
		if controllerutil.ContainsFinalizer(p2CodeSchedulingManifest, finalizer) {
			message := "Performing finalizing operations before deleting P2CodeSchedulingManifest"
			log.Info(message)

			meta.SetStatusCondition(&p2CodeSchedulingManifest.Status.Conditions, metav1.Condition{Type: typeFinalizing, Status: metav1.ConditionTrue, Reason: "Finalizing", Message: message})
			p2CodeSchedulingManifest.Status.Decisions = []schedulingv1alpha1.SchedulingDecision{}
			if err := r.Status().Update(ctx, p2CodeSchedulingManifest); err != nil {
				log.Error(err, "Failed to update P2CodeSchedulingManifest status")
				return ctrl.Result{}, err
			}

			// Deleting ManifestWork resources associated with this P2CodeSchedulingManifest instance
			// Placements and PlacementDecisions are automatically cleaned up as this instance is set as the owner reference for those resources
			if err := r.deleteOwnedManifestWorkList(ctx, p2CodeSchedulingManifest); err != nil {
				log.Error(err, "Failed to perform clean up operations on instance before deleting")
				return ctrl.Result{}, err
			}

			// Fetch updated P2CodeSchedulingManifest instance
			if err := r.Get(ctx, req.NamespacedName, p2CodeSchedulingManifest); err != nil {
				log.Error(err, "Failed to re-fetch P2CodeSchedulingManifest")
				return ctrl.Result{}, err
			}

			message = "Removing finalizer to allow instance to be deleted"
			log.Info(message)

			meta.SetStatusCondition(&p2CodeSchedulingManifest.Status.Conditions, metav1.Condition{Type: typeFinalizing, Status: metav1.ConditionTrue, Reason: "Finalizing", Message: message})
			p2CodeSchedulingManifest.Status.Decisions = []schedulingv1alpha1.SchedulingDecision{}
			if err := r.Status().Update(ctx, p2CodeSchedulingManifest); err != nil {
				log.Error(err, "Failed to update P2CodeSchedulingManifest status")
				return ctrl.Result{}, err
			}

			if ok := controllerutil.RemoveFinalizer(p2CodeSchedulingManifest, finalizer); !ok {
				log.Error(err, "Failed to remove finalizer for P2CodeSchedulingManifest")
				return ctrl.Result{Requeue: true}, nil
			}

			err = r.Update(ctx, p2CodeSchedulingManifest)
			if err != nil {
				log.Error(err, "Failed to update instance and remove finalizer")
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Ensure P2CodeSchedulingManifest instance has a finalizer
	if !controllerutil.ContainsFinalizer(p2CodeSchedulingManifest, finalizer) {
		log.Info("Adding finalizer to P2CodeSchedulingManifest")

		if ok := controllerutil.AddFinalizer(p2CodeSchedulingManifest, finalizer); !ok {
			log.Error(err, "Failed to add finalizer to P2CodeSchedulingManifest instance")
			return ctrl.Result{Requeue: true}, nil
		}

		if err := r.Update(ctx, p2CodeSchedulingManifest); err != nil {
			log.Error(err, "Failed to update P2CodeSchedulingManifest instance with finalizer")
			return ctrl.Result{}, err
		}
	}

	// Analyse manifests to be scheduled
	resourceCollection, err := categoriseManifests(p2CodeSchedulingManifest.Spec.Manifests)
	if err != nil {
		log.Error(err, "Failed to process manifests to be scheduled")
		return ctrl.Result{}, err
	}

	// If no global nor workload annotations are specified examine the workload resource requests to make placement rules
	// TODO calculateWorkloadResourceRequests(resourceCollection.workloads)

	// Check workload annoatations
	for _, workload := range resourceCollection.workloads {
		bundle := Bundle{mainWorkload: workload} // Q is there case when dont make a bundle ??
		// calculateWorkloadResourceRequests(workload, workloadType)

		bundle.ancillaryResourcesManifests = groupAncillaryResources(workload)
	}

	// if global set and workload empty {
	// createPlacement - 1 placement
	//}

	// if global empty and workload empty - everything goes to random cluster

	// if global set and workload set {
	// createPlacements - similar to what have now
	// }

	// if global empty and workload set - bundle workloads to specified cluster and bundle remaining to random cluster

	commonPlacementRules := extractPlacementRules(p2CodeSchedulingManifest.Spec.GlobalAnnotations)
	modifiedWorkloads := listModifiedWorkloads(p2CodeSchedulingManifest.Spec.WorkloadAnnotations)

	for _, manifest := range p2CodeSchedulingManifest.Spec.Manifests {
		object := &unstructured.Unstructured{}
		if err := object.UnmarshalJSON(manifest.Raw); err != nil {
			log.Error(err, "Failed to unmarshal manifest")
			return ctrl.Result{}, err
		}

		placementName := fmt.Sprintf("%s-%s-placement", object.GetName(), strings.ToLower(object.GetKind()))
		manifestWorkName := fmt.Sprintf("%s-%s-manifest", object.GetName(), strings.ToLower(object.GetKind()))

		placement := &clusterv1beta1.Placement{}
		err = r.Get(ctx, types.NamespacedName{Name: placementName, Namespace: p2CodeSchedulingManifest.Namespace}, placement)

		// Create placement for manifest if it doesnt exist
		if err != nil && apierrors.IsNotFound(err) {
			var placementRules []metav1.LabelSelectorRequirement
			index := findWorkload(modifiedWorkloads, object.GetName())
			if index != -1 {
				additionalPlacementRules := extractPlacementRules(p2CodeSchedulingManifest.Spec.WorkloadAnnotations[index].Annotations)
				placementRules = slices.Concat(commonPlacementRules, additionalPlacementRules)
			} else {
				placementRules = commonPlacementRules
			}

			placement = r.generatePlacementForManifest(placementName, p2CodeSchedulingManifest.Namespace, placementRules)

			if err = ctrl.SetControllerReference(p2CodeSchedulingManifest, placement, r.Scheme); err != nil {
				log.Error(err, "Failed to set controller reference for placement")
				return ctrl.Result{}, err
			}

			log.Info("Creating placement", "Placement name", placementName)
			if err = r.Create(ctx, placement); err != nil {
				log.Error(err, "Failed to create Placement")
				return ctrl.Result{}, err
			}

		} else if err != nil {
			log.Error(err, "Failed to fetch Placement")
			return ctrl.Result{}, err
		}

		// Check the placement status for a placement decision
		if placement.Status.Conditions != nil {
			if isPlacementSatisfied(placement.Status.Conditions) {
				placementDecisionName := placement.Status.DecisionGroups[0].Decisions[0]
				placementDecision := &clusterv1beta1.PlacementDecision{}
				err = r.Get(ctx, types.NamespacedName{Name: placementDecisionName, Namespace: p2CodeSchedulingManifest.Namespace}, placementDecision)
				if err != nil {
					log.Error(err, "Failed to get PlacementDecision")
					return ctrl.Result{}, err
				}

				manifestWorkNamespace := placementDecision.Status.Decisions[0].ClusterName
				manifestWork := &workv1.ManifestWork{}
				err = r.Get(ctx, types.NamespacedName{Name: manifestWorkName, Namespace: manifestWorkNamespace}, manifestWork)
				// Create a manifestwork for the manifest if it doesnt exist
				if err != nil && apierrors.IsNotFound(err) {
					newManifestWork := &workv1.ManifestWork{
						ObjectMeta: metav1.ObjectMeta{
							Name:      manifestWorkName,
							Namespace: manifestWorkNamespace,
							Labels: map[string]string{
								ownershipLabel: p2CodeSchedulingManifest.Name,
							},
						},
						Spec: workv1.ManifestWorkSpec{
							Workload: workv1.ManifestsTemplate{
								Manifests: []workv1.Manifest{
									{
										RawExtension: manifest,
									},
								},
							},
						},
					}

					log.Info("Placement decision ready creating ManifestWork", "ManifestWork name", manifestWorkName, "Cluster selected/ManifestWork namespace", manifestWorkNamespace)
					if err = r.Create(ctx, newManifestWork); err != nil {
						log.Error(err, "Failed to create ManifestWork")
						return ctrl.Result{}, err
					}

				} else if err != nil {
					log.Error(err, "Failed to fetch ManifestWork")
					return ctrl.Result{}, err
				}

			} else {
				errorTitle := "placement failed"
				errorMessage := "Unable to find a suitable location for the workload as there are no managed clusters that satisfy the annotations requested"

				err := fmt.Errorf("%s", errorTitle)
				log.Error(err, errorMessage)

				meta.SetStatusCondition(&p2CodeSchedulingManifest.Status.Conditions, metav1.Condition{Type: typeSchedulingFailed, Status: metav1.ConditionTrue, Reason: strcase.ToCamel(errorTitle), Message: errorMessage})
				p2CodeSchedulingManifest.Status.Decisions = []schedulingv1alpha1.SchedulingDecision{}
				if err := r.Status().Update(ctx, p2CodeSchedulingManifest); err != nil {
					log.Error(err, "Failed to update P2CodeSchedulingManifest status")
					return ctrl.Result{}, err
				}

				// All manifests specified within the P2CodeSchedulingManifest must be successfully placed
				// If one manifest fails to be placed, no other manifest should run as the overall scheduling strategy failed
				// Delete all manifestworks associated with this P2CodeSchedulingManifest instance to unschedule any manifest that might previously have been scheduled
				if err := r.deleteOwnedManifestWorkList(ctx, p2CodeSchedulingManifest); err != nil {
					log.Error(err, "Failed to delete ManifestWorks associated with this P2CodeSchedulingManifest instance")
					return ctrl.Result{}, err
				}
				return ctrl.Result{}, err
			}

		} else {
			// If the placement decision is not ready yet run the reconcile loop again in 10 seconds to finish off the controller logic
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}

	}

	return ctrl.Result{}, nil
}

func categoriseManifests(manifests []runtime.RawExtension) (ResourceCollection, error) {
	resourceCollection := ResourceCollection{}
	for _, manifest := range manifests {
		object := &unstructured.Unstructured{}
		if err := object.UnmarshalJSON(manifest.Raw); err != nil {
			return resourceCollection, err
		}

		resource := Resource{kind: object.GetKind(), name: object.GetName(), manifest: manifest}
		switch group := object.GetObjectKind().GroupVersionKind().Group; group {
		// Workload resources are in the core API group
		case "":
			resourceCollection.workloads = append(resourceCollection.workloads, resource)
			// Ancillary services are in the apps API group
		case "apps":
			resourceCollection.ancillaryResources = append(resourceCollection.ancillaryResources, resource)
		}
	}

	return resourceCollection, nil
}

// when iterate over ancillary services check if type service and expand - otherwise check the name against ref in workload
// when iterate over workloads call a factory type fn that returns the right struct to unmarshal to for the kind it is
func groupAncillaryResources(workload Resource) []Resource {
	// need spec of workload

	// deployment, daemonset, job read template
	// statefulset read template, serviceName, volumeClaimTemplate
	// cronjob read jobTemplate.spec

	// have func analysePodTemplateSpec
}

// func that gets the total resource requests from all workloads in bundle
// needs to be generic to use for multiple workloads too when have global bunch

func (r *P2CodeSchedulingManifestReconciler) deleteOwnedManifestWorkList(ctx context.Context, schedulingManifest *schedulingv1alpha1.P2CodeSchedulingManifest) error {
	manifestWorkList := &workv1.ManifestWorkList{}
	labelSelector, err := labels.Parse(fmt.Sprintf("%s=%s", ownershipLabel, schedulingManifest.Name))

	if err != nil {
		return err
	}

	listOptions := client.ListOptions{
		LabelSelector: labelSelector,
	}

	if err := r.List(ctx, manifestWorkList, &listOptions); err != nil {
		return err
	}

	for _, manifest := range manifestWorkList.Items {
		if err := r.Delete(ctx, &manifest); err != nil {
			return err
		}
	}

	return nil
}

func isPlacementSatisfied(conditions []metav1.Condition) bool {
	satisfied := false
	for _, condition := range conditions {
		if condition.Type == "PlacementSatisfied" {
			if condition.Status == "True" {
				satisfied = true
			}
			break
		}
	}
	return satisfied
}

func (r *P2CodeSchedulingManifestReconciler) generatePlacementForManifest(name string, namespace string, placementRules []metav1.LabelSelectorRequirement) *clusterv1beta1.Placement {
	var numClusters int32 = 1

	placement := &clusterv1beta1.Placement{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: clusterv1beta1.PlacementSpec{
			NumberOfClusters: &numClusters,
			ClusterSets: []string{
				"global",
			},
			Predicates: []clusterv1beta1.ClusterPredicate{
				{
					RequiredClusterSelector: clusterv1beta1.ClusterSelector{
						ClaimSelector: clusterv1beta1.ClusterClaimSelector{
							MatchExpressions: placementRules,
						},
					},
				},
			},
		},
	}

	return placement
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
		Owns(&clusterv1beta1.Placement{}).
		Owns(&workv1.ManifestWork{}).
		Complete(r)
}
