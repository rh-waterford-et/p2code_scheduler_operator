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
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/iancoleman/strcase"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	schedulingv1alpha1 "github.com/rh-waterford-et/p2code-scheduler-operator/api/v1alpha1"
	clusterv1beta1 "open-cluster-management.io/api/cluster/v1beta1"
	workv1 "open-cluster-management.io/api/work/v1"
)

const finalizer = "scheduling.p2code.eu/finalizer"
const ownershipLabel = "scheduling.p2code.eu/owner"
const P2CodeSchedulerNamespace = "p2code-scheduler-system"

const fetchFailure = "Failed to fetch P2CodeSchedulingManifest"
const updateFailure = "Failed to update P2CodeSchedulingManifest status"
const configurationIssue = "There is a configuration issue in the P2CodeSchedulingManifest instance"

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

// P2CodeSchedulingManifestReconciler reconciles a P2CodeSchedulingManifest object
type P2CodeSchedulingManifestReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	Bundles  map[string][]*Bundle
}

// +kubebuilder:rbac:groups=scheduling.p2code.eu,resources=p2codeschedulingmanifests,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=scheduling.p2code.eu,resources=p2codeschedulingmanifests/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=scheduling.p2code.eu,resources=p2codeschedulingmanifests/finalizers,verbs=update
// +kubebuilder:rbac:groups=cluster.open-cluster-management.io,resources=placements,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cluster.open-cluster-management.io,resources=placementdecisions,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups=cluster.open-cluster-management.io,resources=managedclustersets,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.open-cluster-management.io,resources=managedclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.open-cluster-management.io,resources=managedclustersetbindings,verbs=get;list;watch
// +kubebuilder:rbac:groups=work.open-cluster-management.io,resources=manifestworks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ac3.redhat.com,resources=multiclusternetworks,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the P2CodeSchedulingManifest object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.19.0/pkg/reconcile

// nolint:cyclop // not to concerned about cognitive complexity (brainfreeze)
func (r *P2CodeSchedulingManifestReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	if r.Bundles == nil {
		r.Bundles = make(map[string][]*Bundle)
	}

	// Get P2CodeSchedulingManifest instance
	p2CodeSchedulingManifest := &schedulingv1alpha1.P2CodeSchedulingManifest{}
	err := r.Get(ctx, req.NamespacedName, p2CodeSchedulingManifest)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Stop reconciliation if the resource is not found or has been deleted
			return ctrl.Result{}, nil
		}
		log.Error(err, fetchFailure)
		return ctrl.Result{}, fmt.Errorf("%w", err)
	}

	// If the status is empty set the status as scheduling in progress
	if len(p2CodeSchedulingManifest.Status.Conditions) == 0 {
		condition := metav1.Condition{Type: schedulingInProgress, Status: metav1.ConditionTrue, Reason: schedulingInProgress, Message: "Analysing scheduling requirements"}
		if err := r.UpdateStatus(ctx, p2CodeSchedulingManifest, condition, []schedulingv1alpha1.SchedulingDecision{}); err != nil {
			log.Error(err, updateFailure)
			return ctrl.Result{}, fmt.Errorf("%w", err)
		}
	}

	// Validate that the P2CodeSchedulingManifest instance is in the correct namespace
	if req.Namespace != P2CodeSchedulerNamespace {
		errorTitle := "incorrect namespace"
		errorMessage := "Ignoring resource as it is not in the p2code-scheduler-system namespace"

		err := fmt.Errorf("%s", errorTitle)
		log.Error(err, errorMessage)

		condition := metav1.Condition{Type: misconfigured, Status: metav1.ConditionTrue, Reason: strcase.ToCamel(errorTitle), Message: errorMessage}
		if err := r.UpdateStatus(ctx, p2CodeSchedulingManifest, condition, []schedulingv1alpha1.SchedulingDecision{}); err != nil {
			log.Error(err, updateFailure)
			return ctrl.Result{}, fmt.Errorf("%w", err)
		}

		return ctrl.Result{}, nil
	}

	// Ensure P2CodeSchedulingManifest instance has a finalizer
	if !controllerutil.ContainsFinalizer(p2CodeSchedulingManifest, finalizer) {
		// Refetch P2CodeSchedulingManifest instance to get the latest state of the resource
		if err := r.Get(ctx, req.NamespacedName, p2CodeSchedulingManifest); err != nil {
			log.Error(err, fetchFailure)
			return ctrl.Result{}, fmt.Errorf("%w", err)
		}

		if ok := controllerutil.AddFinalizer(p2CodeSchedulingManifest, finalizer); !ok {
			log.Error(err, "Failed to add finalizer to P2CodeSchedulingManifest instance")
			return ctrl.Result{Requeue: true}, nil
		}

		if err := r.Update(ctx, p2CodeSchedulingManifest); err != nil {
			log.Error(err, "Failed to update P2CodeSchedulingManifest instance with finalizer")
			return ctrl.Result{}, fmt.Errorf("%w", err)
		}

		log.Info("Finalizer added to P2CodeSchedulingManifest")
	}

	// Check if P2CodeSchedulingManifest instance is marked for deletion
	// nolint:nestif // not to concerned about cognitive complexity (brainfreeze)
	if p2CodeSchedulingManifest.GetDeletionTimestamp() != nil {
		if controllerutil.ContainsFinalizer(p2CodeSchedulingManifest, finalizer) {
			message := "Performing finalizing operations before deleting P2CodeSchedulingManifest"
			log.Info(message)

			condition := metav1.Condition{Type: finalizing, Status: metav1.ConditionTrue, Reason: "Finalizing", Message: message}
			if err := r.UpdateStatus(ctx, p2CodeSchedulingManifest, condition, []schedulingv1alpha1.SchedulingDecision{}); err != nil {
				log.Error(err, updateFailure)
				return ctrl.Result{}, fmt.Errorf("%w", err)
			}

			// Deleting ManifestWork resources associated with this P2CodeSchedulingManifest instance
			// Placements and PlacementDecisions are automatically cleaned up as this instance is set as the owner reference for those resources
			if err := r.deleteOwnedManifestWorkList(ctx, p2CodeSchedulingManifest.Name); err != nil {
				log.Error(err, "Failed to perform clean up operations on instance before deleting")
				return ctrl.Result{}, fmt.Errorf("%w", err)
			}

			r.deleteBundles(p2CodeSchedulingManifest.Name)

			message = "Removing finalizer to allow instance to be deleted"
			log.Info(message)

			condition = metav1.Condition{Type: finalizing, Status: metav1.ConditionTrue, Reason: finalizing, Message: message}
			if err := r.UpdateStatus(ctx, p2CodeSchedulingManifest, condition, []schedulingv1alpha1.SchedulingDecision{}); err != nil {
				log.Error(err, updateFailure)
				return ctrl.Result{}, fmt.Errorf("%w", err)
			}

			if ok := controllerutil.RemoveFinalizer(p2CodeSchedulingManifest, finalizer); !ok {
				log.Error(err, "Failed to remove finalizer for P2CodeSchedulingManifest")
				return ctrl.Result{Requeue: true}, nil
			}

			err = r.Update(ctx, p2CodeSchedulingManifest)
			if err != nil {
				log.Error(err, "Failed to update instance and remove finalizer")
				return ctrl.Result{}, fmt.Errorf("%w", err)
			}
		}
		return ctrl.Result{}, nil
	}

	// Ensure all annotations specified are supported by the scheduler
	ok, err := ValidateAnnotationsSupported(p2CodeSchedulingManifest)
	if !ok {
		errorMessage := "Unsupported annotations found"
		log.Error(err, errorMessage)

		condition := metav1.Condition{Type: misconfigured, Status: metav1.ConditionTrue, Reason: "UnsupportedAnnotation", Message: errorMessage + ", " + err.Error()}
		if err := r.UpdateStatus(ctx, p2CodeSchedulingManifest, condition, []schedulingv1alpha1.SchedulingDecision{}); err != nil {
			log.Error(err, updateFailure)
			return ctrl.Result{}, fmt.Errorf("%w", err)
		}

		return ctrl.Result{}, nil
	}

	// Extract the target cluster set and optional target cluster from the P2CodeSchedulingManifest instance
	targetClusterSet, targetCluster, err := ExtractTarget(p2CodeSchedulingManifest.Spec.GlobalAnnotations)
	if err != nil {
		errorMessage := "Target information missing from the scheduling manifest"
		log.Error(err, errorMessage)

		condition := metav1.Condition{Type: misconfigured, Status: metav1.ConditionTrue, Reason: "MissingTarget", Message: errorMessage + ", " + err.Error()}
		if err := r.UpdateStatus(ctx, p2CodeSchedulingManifest, condition, []schedulingv1alpha1.SchedulingDecision{}); err != nil {
			log.Error(err, updateFailure)
			return ctrl.Result{}, fmt.Errorf("%w", err)
		}

		return ctrl.Result{}, nil
	}

	// Validate that the cluster set exists
	exists, err := r.doesManagedClusterSetExist(ctx, targetClusterSet)
	if err != nil {
		log.Error(err, "An error occurred while validating the managed cluster set")
		return ctrl.Result{}, fmt.Errorf("%w", err)
	}

	if !exists {
		message := fmt.Sprintf("Cannot find a managed cluster set with the name %s", targetClusterSet)
		condition := metav1.Condition{Type: schedulingFailed, Status: metav1.ConditionTrue, Reason: "InvalidTarget", Message: message}
		if err := r.UpdateStatus(ctx, p2CodeSchedulingManifest, condition, []schedulingv1alpha1.SchedulingDecision{}); err != nil {
			log.Error(err, updateFailure)
			return ctrl.Result{}, fmt.Errorf("%w", err)
		}

		log.Info(message)
		return ctrl.Result{}, nil
	}

	// Ensure the cluster set is not empty
	empty, err := r.isClusterSetEmpty(ctx, targetClusterSet)
	if err != nil {
		log.Error(err, "An error occurred while examining the cluster set")
		return ctrl.Result{}, fmt.Errorf("%w", err)
	}

	if empty {
		message := fmt.Sprintf("The managed cluster set selected (%s) is empty", targetClusterSet)
		condition := metav1.Condition{Type: schedulingFailed, Status: metav1.ConditionTrue, Reason: "InvalidTarget", Message: message}
		if err := r.UpdateStatus(ctx, p2CodeSchedulingManifest, condition, []schedulingv1alpha1.SchedulingDecision{}); err != nil {
			log.Error(err, updateFailure)
			return ctrl.Result{}, fmt.Errorf("%w", err)
		}

		log.Info(message)
		return ctrl.Result{}, nil
	}

	// Ensure the cluster set specified is bound to the controller namespace
	bound, err := r.isClusterSetBound(ctx, targetClusterSet)
	if err != nil {
		log.Error(err, "An error occurred while examining the cluster set bindings")
		return ctrl.Result{}, fmt.Errorf("%w", err)
	}

	if !bound {
		message := fmt.Sprintf("The scheduler is not authorized to access the %s managed cluster set", targetClusterSet)
		condition := metav1.Condition{Type: schedulingFailed, Status: metav1.ConditionTrue, Reason: "InaccessibleManagedClusterSet", Message: message}
		if err := r.UpdateStatus(ctx, p2CodeSchedulingManifest, condition, []schedulingv1alpha1.SchedulingDecision{}); err != nil {
			log.Error(err, updateFailure)
			return ctrl.Result{}, fmt.Errorf("%w", err)
		}

		log.Info(message)
		return ctrl.Result{}, nil
	}

	// If a target cluster is specified all manifests are bundled together and sent to the given cluster
	// Otherwise the scheduler identifies a suitable cluster for each workload in the P2CodeSchedulingManifest and bundles the workload with its ancillary resources
	// nolint:nestif // not to concerned about cognitive complexity (brainfreeze)
	if targetCluster != "" {
		// Validate that the cluster set specified contains the target cluster
		member, err := r.isManagedClusterInSet(ctx, targetClusterSet, targetCluster)
		if err != nil {
			log.Error(err, "An error occurred while validating the target cluster's cluster set membership")
			return ctrl.Result{}, fmt.Errorf("%w", err)
		}

		if !member {
			message := fmt.Sprintf("Cannot find a managed cluster with the name %s in the %s managed cluster set", targetCluster, targetClusterSet)
			condition := metav1.Condition{Type: schedulingFailed, Status: metav1.ConditionTrue, Reason: "InvalidTarget", Message: message}
			if err := r.UpdateStatus(ctx, p2CodeSchedulingManifest, condition, []schedulingv1alpha1.SchedulingDecision{}); err != nil {
				log.Error(err, updateFailure)
				return ctrl.Result{}, fmt.Errorf("%w", err)
			}

			log.Info(message)
			return ctrl.Result{}, nil
		}

		// Extract resources from the P2CodeSchedulingManifest instance to populate the bundle
		resources, err := bulkConvertToResourceSet(p2CodeSchedulingManifest.Spec.Manifests)
		if err != nil {
			log.Error(err, "Failed to process manifests to be scheduled")
			return ctrl.Result{}, fmt.Errorf("%w", err)
		}

		// Create a bundle if necessary and set the clusterName to the targetCluster specified
		_, err = r.getBundle(p2CodeSchedulingManifest.Name, p2CodeSchedulingManifest.Name)
		if err != nil {
			bundle := &Bundle{name: p2CodeSchedulingManifest.Name, resources: resources, clusterName: targetCluster}
			r.addBundle(bundle, p2CodeSchedulingManifest.Name)
		}

	} else {
		err := r.buildBundle(p2CodeSchedulingManifest)
		var misconfiguredManifestErr *MisconfiguredManifestError
		var resourceNotFoundErr *ResourceNotFoundError
		if errors.As(err, &misconfiguredManifestErr) || errors.As(err, &resourceNotFoundErr) {
			log.Error(err, configurationIssue)
			condition := metav1.Condition{Type: misconfigured, Status: metav1.ConditionTrue, Reason: "MisconfiguredManifest", Message: strings.ToUpper(err.Error())}
			if err := r.UpdateStatus(ctx, p2CodeSchedulingManifest, condition, []schedulingv1alpha1.SchedulingDecision{}); err != nil {
				log.Error(err, updateFailure)
				return ctrl.Result{}, fmt.Errorf("%w", err)
			}

			// End reconciliation if the P2CodeSchedulingManifest is misconfigured
			return ctrl.Result{}, nil
		} else if err != nil {
			log.Error(err, "Failed to perform bundling")
			return ctrl.Result{}, fmt.Errorf("%w", err)
		}

		for _, bundle := range r.Bundles[p2CodeSchedulingManifest.Name] {
			// Retrieve the placement associated with this bundle
			placement, err := r.getAssociatedPlacement(ctx, bundle, p2CodeSchedulingManifest)
			if err != nil {
				errorMessage := fmt.Sprintf("Failed to fetch the placement associated with the %s bundle", bundle.name)
				log.Error(err, errorMessage)
				return ctrl.Result{}, fmt.Errorf("%w", err)
			}

			// Ensure the placement has a placement decision
			if len(placement.Status.Conditions) < 1 {
				message := fmt.Sprintf("Placement decision not ready yet for %s placement, requeuing", placement.Name)
				log.Info(message)
				return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
			}

			// Check if the placement was satisfied and a suitable cluster found for the bundle
			placementSatisfiedCondition := meta.FindStatusCondition(placement.Status.Conditions, "PlacementSatisfied")
			if placementSatisfiedCondition.Status == metav1.ConditionTrue {
				clusterName, err := r.getSelectedCluster(ctx, *placement)
				if err != nil {
					return ctrl.Result{}, fmt.Errorf("unable to read placement decision for %s placement", placement.Name)
				}

				bundle.clusterName = clusterName
			} else {
				condition, err := r.extractFailedCondition(ctx, *placementSatisfiedCondition, placement.Spec.ClusterSets[0])
				if err != nil {
					log.Error(err, "Failed to build status condition")
					return ctrl.Result{}, fmt.Errorf("%w", err)
				}

				if err := r.UpdateStatus(ctx, p2CodeSchedulingManifest, *condition, []schedulingv1alpha1.SchedulingDecision{}); err != nil {
					log.Error(err, updateFailure)
					return ctrl.Result{}, fmt.Errorf("%w", err)
				}

				// End reconciliation if the placement cannot be satisfied
				message := fmt.Sprintf("Scheduling failed as placement cannot be satisfied: %s", condition.Message)
				log.Info(message)
				return ctrl.Result{}, nil
			}
		}

		schedulingDecisions := r.getSchedulingDecisions(p2CodeSchedulingManifest)
		condition := metav1.Condition{Type: schedulingInProgress, Status: metav1.ConditionTrue, Reason: "PlacementDecisionReady", Message: "A suitable cluster has been identified for each workload"}
		if err := r.UpdateStatus(ctx, p2CodeSchedulingManifest, condition, schedulingDecisions); err != nil {
			log.Error(err, updateFailure)
			return ctrl.Result{}, fmt.Errorf("%w", err)
		}
	}

	// All manifests specified within the P2CodeSchedulingManifest spec must be successfully placed
	// If one manifest fails to be placed, no other manifest should run as the overall scheduling strategy failed
	// Since all manifests should be contained within a bundle and a suitable cluster has been found for each bundle the corresponding ManifestWorks can be generated
	manifestWorks := []workv1.ManifestWork{}
	placedManifests := 0
	for _, bundle := range r.Bundles[p2CodeSchedulingManifest.Name] {
		placedManifests += len(bundle.resources)

		manifestWork := &workv1.ManifestWork{}
		manifestWorkName := fmt.Sprintf("%s-%s-bundle", p2CodeSchedulingManifest.Name, bundle.name)
		err = r.Get(ctx, types.NamespacedName{Name: manifestWorkName, Namespace: bundle.clusterName}, manifestWork)
		// Define ManifestWork to be created if a ManifestWork doesnt exist for this bundle
		if err != nil && apierrors.IsNotFound(err) {
			newManifestWork, err := r.generateManifestWorkForBundle(manifestWorkName, bundle.clusterName, p2CodeSchedulingManifest.Name, bundle.resources)
			var misconfiguredManifestErr *MisconfiguredManifestError
			if errors.As(err, &misconfiguredManifestErr) {
				log.Error(err, configurationIssue)

				condition := metav1.Condition{Type: misconfigured, Status: metav1.ConditionTrue, Reason: "MisconfiguredManifest", Message: strings.ToUpper(err.Error())}
				if err := r.UpdateStatus(ctx, p2CodeSchedulingManifest, condition, []schedulingv1alpha1.SchedulingDecision{}); err != nil {
					log.Error(err, updateFailure)
					return ctrl.Result{}, fmt.Errorf("%w", err)
				}

				// End reconciliation if the P2CodeSchedulingManifest is misconfigured
				return ctrl.Result{}, nil

			} else if err != nil {
				log.Error(err, "Failed to generate ManifestWork")
				return ctrl.Result{}, fmt.Errorf("%w", err)
			}

			manifestWorks = append(manifestWorks, newManifestWork)
		}
	}

	log.Info("Ready to push ManifestWork(s) to schedule the workload to the identified cluster")

	// Create ManifestWorks
	// TODO Do need to delete manifest works already deployed if one fails to create - use deleteOwnedManifestWorkList func
	for _, manifestWork := range manifestWorks {
		if err = r.Create(ctx, &manifestWork); err != nil {
			log.Error(err, "Failed to create ManifestWork")
			return ctrl.Result{}, fmt.Errorf("%w", err)
		}
	}

	// Fetch list of applied ManifestWorks owned by this P2CodeSchedulingManifest instance
	manifestWorkList, err := r.getOwnedManifestWorkList(ctx, p2CodeSchedulingManifest.Name)
	if err != nil {
		log.Error(err, "Failed to fetch list of ManifestWorks owned by the P2CodeSchedulingManifest instance")
		return ctrl.Result{}, fmt.Errorf("%w", err)
	}

	// Ensure all ManifestWorks owned by this P2CodeSchedulingManifest instance are applied, if not run the reconcile loop again in 10 seconds to complete reconciliation
	if len(r.Bundles[p2CodeSchedulingManifest.Name]) != len(manifestWorkList.Items) {
		log.Info("Waiting for all ManifestWorks to be applied, requeuing")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// Validate that the ManifestWork is successfully applied to the selected cluster
	for _, manifestWork := range manifestWorkList.Items {
		err := r.validateManifestWorkApplied(manifestWork)
		var manifestWorkFailedErr *ManifestWorkFailedError
		var manifestWorkNotReady *ManifestWorkNotReadyError
		// nolint:gocritic // not to hassled about 3 if then else statements
		if errors.As(err, &manifestWorkFailedErr) {
			log.Error(err, "Failed to apply ManifestWork")

			condition := metav1.Condition{Type: schedulingFailed, Status: metav1.ConditionTrue, Reason: "ManifestWorkFailed", Message: strings.ToUpper(err.Error())}
			schedulingDecisions := r.getSchedulingDecisions(p2CodeSchedulingManifest)
			if err := r.UpdateStatus(ctx, p2CodeSchedulingManifest, condition, schedulingDecisions); err != nil {
				log.Error(err, updateFailure)
				return ctrl.Result{}, fmt.Errorf("%w", err)
			}

			return ctrl.Result{}, nil

		} else if errors.As(err, &manifestWorkNotReady) {
			message := fmt.Sprintf("Waiting for %s ManifestWork to be ready: %s, requeuing", manifestWork.Name, err.Error())
			log.Info(message)
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		} else if err != nil {
			log.Error(err, "Error occurred while validating the state of the ManifestWork applied")
			return ctrl.Result{}, fmt.Errorf("%w", err)
		}
	}

	// Check for orphaned manifests
	// Orphaned manifests can arise if there are ancillary services that are not referenced by a workload resource and are therefore not added to a bundle
	// If there are more manifests in the CR than the count of manifests across all ManifestWorks this indicates that some manifests are unaccounted for
	// It is unlikely that the values are equal since an ancillary manifest can be included in many ManifestWorks
	if len(p2CodeSchedulingManifest.Spec.Manifests) > placedManifests {
		warningMessage := "All workloads have been successfully scheduled however orphaned manifests have been found, ensure all ancillary resources (services, configmaps, secrets, etc) are referenced by a workload resource"
		log.Info(warningMessage)

		condition := metav1.Condition{Type: tentativelyScheduled, Status: metav1.ConditionTrue, Reason: "OrphanedManifest", Message: warningMessage}
		if err := r.UpdateStatus(ctx, p2CodeSchedulingManifest, condition, []schedulingv1alpha1.SchedulingDecision{}); err != nil {
			log.Error(err, updateFailure)
			return ctrl.Result{}, fmt.Errorf("%w", err)
		}

		// End reconciliation
		return ctrl.Result{}, nil
	}

	schedulingDecisions := r.getSchedulingDecisions(p2CodeSchedulingManifest)
	log.Info("All workloads have been successfully scheduled to a suitable cluster, checking if network links need to be created")

	connectionCount := 0
	for _, bundle := range r.Bundles[p2CodeSchedulingManifest.Name] {
		connectionCount += len(bundle.externalConnections)
	}

	// nolint:nestif // not to concerned about cognitive complexity (brainfreeze)
	if connectionCount != 0 {
		// Ensure the MultiClusterNetwork resource is installed
		isInstalled, err := isMultiClusterNetworkInstalled()
		if err != nil {
			log.Error(err, "Error occurred while checking if the MultiClusterNetwork resource is present")
			return ctrl.Result{}, fmt.Errorf("%w", err)
		}

		// Update the state informing the user that all workloads have been scheduled according to their requests
		// Emphasise the fact that network connectivity between components cannot be guaranteed as it is not possible to create the necessary MultiClusterLinks without the resource being present
		if !isInstalled {
			message := "All workloads have been successfully scheduled, however network connectivity between components cannot be guaranteed as it is not possible to create the necessary MultiClusterLinks without the resource being present"
			log.Info(message)

			condition := metav1.Condition{Type: unreliablyScheduled, Status: metav1.ConditionTrue, Reason: "MultiClusterNetworkResourceMissing", Message: message}
			if err := r.UpdateStatus(ctx, p2CodeSchedulingManifest, condition, schedulingDecisions); err != nil {
				log.Error(err, updateFailure)
				return ctrl.Result{}, fmt.Errorf("%w", err)
			}

			// End reconciliation
			return ctrl.Result{}, nil
		}

		connections, err := r.getNetworkConnections(p2CodeSchedulingManifest)
		if err != nil {
			log.Error(err, "Error occurred while retrieving network connections")
			return ctrl.Result{}, err
		}

		// Create MultiClusterLinks and update the MultiClusterNetwork resource
		err = r.registerNetworkLinks(ctx, connections)
		if err != nil {
			log.Error(err, "Error occurred while registering network links")
			return ctrl.Result{}, err
		}

		// TODO add status update to mark that network links have been created

		// Check if there is a link registered for every externalConnection
		if len(connections) != connectionCount {
			message := "All workloads have been successfully scheduled, however network connectivity between components cannot be guaranteed"
			log.Info(message)

			condition := metav1.Condition{Type: unreliablyScheduled, Status: metav1.ConditionTrue, Reason: unreliablyScheduled, Message: message}
			if err := r.UpdateStatus(ctx, p2CodeSchedulingManifest, condition, schedulingDecisions); err != nil {
				log.Error(err, updateFailure)
				return ctrl.Result{}, err
			}

			// End reconciliation
			return ctrl.Result{}, nil
		} else {
			log.Info("Network links successfully registered")
		}
	}

	message := "All workloads have been successfully scheduled to a suitable cluster"
	condition := metav1.Condition{Type: schedulingSuccessful, Status: metav1.ConditionTrue, Reason: schedulingSuccessful, Message: message}
	if err := r.UpdateStatus(ctx, p2CodeSchedulingManifest, condition, schedulingDecisions); err != nil {
		log.Error(err, updateFailure)
		return ctrl.Result{}, fmt.Errorf("%w", err)
	}

	log.Info("Reconciliation complete")
	return ctrl.Result{}, nil
}

// TODO might as well analyse the resource requests in here when already extracted add a field for resource requests cpu etc to the bundle
// nolint:cyclop // not to concerned about cognitive complexity (brainfreeze)
func analyseWorkload(workload *Resource, ancillaryResources ResourceSet) (ResourceSet, []ServicePortPair, error) {
	resources := ResourceSet{}

	if workload.metadata.namespace == "" {
		errorMessage := fmt.Sprintf("no namespace defined for the workload %s", workload.metadata.name)
		return ResourceSet{}, []ServicePortPair{}, &MisconfiguredManifestError{errorMessage}
	}

	if workload.metadata.namespace != "default" {
		namespaceResource, err := ancillaryResources.Find(workload.metadata.namespace, "Namespace")
		if err != nil {
			return ResourceSet{}, []ServicePortPair{}, fmt.Errorf("%w", err)
		}

		resources.Add(namespaceResource)
	}

	rs, externalConnections, err := analysePodSpec(workload, ancillaryResources)
	if err != nil {
		return ResourceSet{}, []ServicePortPair{}, fmt.Errorf("%w", err)
	} else {
		resources = append(resources, rs...)
	}

	// TODO test this case
	// Add services to the bundle
	// nolint:nestif // not to concerned about cognitive complexity (brainfreeze)
	if workload.metadata.groupVersionKind.Kind == "StatefulSet" {
		// If the workload is a StatefulSet the associated service can be found in the ServiceName field of its spec
		// volumeClaimTemplate is a list of pvc, not reference to pvc, look at storage classes
		statefulset := &appsv1.StatefulSet{}
		if err := json.Unmarshal(workload.manifest.Raw, statefulset); err != nil {
			return ResourceSet{}, []ServicePortPair{}, fmt.Errorf("%w", err)
		}

		svcResource, err := ancillaryResources.Find(statefulset.Spec.ServiceName, "Service")
		if err != nil {
			return ResourceSet{}, []ServicePortPair{}, fmt.Errorf("%w", err)
		}

		resources.Add(svcResource)
	} else {
		// Get a list of all services and check if the service selector matches the labels on the workload
		services := ancillaryResources.FilterByKind("Service")
		for _, service := range services {
			svc := &corev1.Service{}
			if err := json.Unmarshal(service.manifest.Raw, svc); err != nil {
				return ResourceSet{}, []ServicePortPair{}, fmt.Errorf("%w", err)
			}

			for k, v := range svc.Spec.Selector {
				value, ok := workload.metadata.labels[k]

				if ok && value == v {
					resources.Add(service)
				}
			}
		}
	}

	return resources, externalConnections, nil
}

// nolint:cyclop // not to concerned about cognitive complexity (brainfreeze)
func analysePodSpec(workload *Resource, ancillaryResources ResourceSet) (ResourceSet, []ServicePortPair, error) {
	resources := ResourceSet{}
	externalConnections := []ServicePortPair{}

	podSpec, err := extractPodSpec(*workload)
	if err != nil {
		return ResourceSet{}, []ServicePortPair{}, fmt.Errorf("%w", err)
	}

	// Open question is there a need to consider nodeSelector, tolerations

	// TODO analyse securityContextProfile under container and pod for later version

	// Later could support other types and check for aws and azure types
	// Could also include storage classes

	for _, volume := range podSpec.Volumes {
		if volume.PersistentVolumeClaim != nil {
			pvcResource, err := ancillaryResources.Find(volume.PersistentVolumeClaim.ClaimName, "PersistentVolumeClaim")
			if err != nil {
				return ResourceSet{}, []ServicePortPair{}, fmt.Errorf("%w", err)
			}

			resources.Add(pvcResource)
		}

		// Later check for storage class
		// pvc := &corev1.PersistentVolumeClaim{}
		// if err := json.Unmarshal(pvcResource.manifest.Raw, pvc); err != nil {
		// 	return err
		// }

		if volume.ConfigMap != nil {
			cmResource, err := ancillaryResources.Find(volume.ConfigMap.Name, "ConfigMap")
			if err != nil {
				return ResourceSet{}, []ServicePortPair{}, fmt.Errorf("%w", err)
			}

			resources.Add(cmResource)
		}

		if volume.Secret != nil {
			secretResource, err := ancillaryResources.Find(volume.Secret.SecretName, "Secret")
			if err != nil {
				return ResourceSet{}, []ServicePortPair{}, fmt.Errorf("%w", err)
			}

			resources.Add(secretResource)
		}
	}

	if podSpec.ServiceAccountName != "" {
		saResource, err := ancillaryResources.Find(podSpec.ServiceAccountName, "ServiceAccount")
		if err != nil {
			return ResourceSet{}, []ServicePortPair{}, fmt.Errorf("%w", err)
		}

		resources.Add(saResource)
	}

	// Examine Containers and InitContainers for ancillary resources
	containers := podSpec.Containers
	containers = append(containers, podSpec.InitContainers...)

	// TODO examine resource requests for container
	for _, container := range containers {
		for _, envSource := range container.EnvFrom {
			if envSource.ConfigMapRef != nil {
				cmResource, err := ancillaryResources.Find(envSource.ConfigMapRef.Name, "ConfigMap")
				if err != nil {
					return ResourceSet{}, []ServicePortPair{}, fmt.Errorf("%w", err)
				}

				resources.Add(cmResource)
			}

			if envSource.SecretRef != nil {
				secretResource, err := ancillaryResources.Find(envSource.SecretRef.Name, "Secret")
				if err != nil {
					return ResourceSet{}, []ServicePortPair{}, fmt.Errorf("%w", err)
				}

				resources.Add(secretResource)
			}
		}

		for _, envVar := range container.Env {
			if envVar.ValueFrom != nil {
				if envVar.ValueFrom.ConfigMapKeyRef != nil {
					cmResource, err := ancillaryResources.Find(envVar.ValueFrom.ConfigMapKeyRef.Name, "ConfigMap")
					if err != nil {
						return ResourceSet{}, []ServicePortPair{}, fmt.Errorf("%w", err)
					}

					resources.Add(cmResource)
				}

				if envVar.ValueFrom.SecretKeyRef != nil {
					secretResource, err := ancillaryResources.Find(envVar.ValueFrom.SecretKeyRef.Name, "Secret")
					if err != nil {
						return ResourceSet{}, []ServicePortPair{}, fmt.Errorf("%w", err)
					}

					resources.Add(secretResource)
				}
			}

			if envVar.Value != "" && isServiceNamePortReference(envVar.Value) {
				// If the workload contains an environment variable that references a service append it to the externalConnections list
				// This list is later used to create MultiClusterNetwork resources to expose services across clusters if required
				serviceName := strings.Split(envVar.Value, ":")[0]
				port, err := strconv.Atoi(strings.Split(envVar.Value, ":")[1])

				if err != nil {
					return ResourceSet{}, []ServicePortPair{}, fmt.Errorf("%w", err)
				}

				externalConnections = append(externalConnections, ServicePortPair{serviceName: serviceName, port: port})
			}
		}
	}

	return resources, externalConnections, nil
}

func (r *P2CodeSchedulingManifestReconciler) getAssociatedPlacement(ctx context.Context, bundle *Bundle, p2CodeSchedulingManifest *schedulingv1alpha1.P2CodeSchedulingManifest) (*clusterv1beta1.Placement, error) {
	// Use the p2CodeSchedulingManifest name and bundle name to build a unique Placement name
	placementName := fmt.Sprintf("%s-%s-bundle", p2CodeSchedulingManifest.Name, bundle.name)
	placement := &clusterv1beta1.Placement{}
	err := r.Get(ctx, types.NamespacedName{Name: placementName, Namespace: P2CodeSchedulerNamespace}, placement)

	// Create a placement for the bundle if it doesnt exist
	if err != nil && apierrors.IsNotFound(err) {
		// Get target managed cluster set
		clusterSet, _, err := ExtractTarget(p2CodeSchedulingManifest.Spec.GlobalAnnotations)
		if err != nil {
			// log.Error(err, "Failed to extract target information")
			return nil, fmt.Errorf("%w", err)
		}

		return r.createPlacement(ctx, placementName, clusterSet, bundle.placementRequests, p2CodeSchedulingManifest)
	}

	return placement, nil
}

func (r *P2CodeSchedulingManifestReconciler) getOwnedManifestWorkList(ctx context.Context, ownerLabel string) (workv1.ManifestWorkList, error) {
	manifestWorkList := &workv1.ManifestWorkList{}
	labelSelector, err := labels.Parse(fmt.Sprintf("%s=%s", ownershipLabel, ownerLabel))

	if err != nil {
		return workv1.ManifestWorkList{}, fmt.Errorf("%w", err)
	}

	listOptions := client.ListOptions{
		LabelSelector: labelSelector,
	}

	if err := r.List(ctx, manifestWorkList, &listOptions); err != nil {
		return workv1.ManifestWorkList{}, fmt.Errorf("%w", err)
	}

	return *manifestWorkList, nil
}

func (r *P2CodeSchedulingManifestReconciler) deleteOwnedManifestWorkList(ctx context.Context, ownerLabel string) error {
	manifestWorkList, err := r.getOwnedManifestWorkList(ctx, ownerLabel)
	if err != nil {
		return fmt.Errorf("%w", err)
	}

	for _, manifest := range manifestWorkList.Items {
		if err := r.Delete(ctx, &manifest); err != nil {
			return fmt.Errorf("%w", err)
		}
	}

	return nil
}

func (r *P2CodeSchedulingManifestReconciler) createPlacement(ctx context.Context, placementName string, clusterSet string, clusterPredicates []metav1.LabelSelectorRequirement, controllerReference metav1.Object) (*clusterv1beta1.Placement, error) {
	var numClusters int32 = 1

	spec := clusterv1beta1.PlacementSpec{
		NumberOfClusters: &numClusters,
		ClusterSets: []string{
			clusterSet,
		},
	}

	if clusterPredicates != nil {
		spec.Predicates = []clusterv1beta1.ClusterPredicate{
			{
				RequiredClusterSelector: clusterv1beta1.ClusterSelector{
					ClaimSelector: clusterv1beta1.ClusterClaimSelector{
						MatchExpressions: clusterPredicates,
					},
				},
			},
		}
	}

	placement := &clusterv1beta1.Placement{
		ObjectMeta: metav1.ObjectMeta{
			Name:      placementName,
			Namespace: P2CodeSchedulerNamespace,
		},
		Spec: spec,
	}

	if err := ctrl.SetControllerReference(controllerReference, placement, r.Scheme); err != nil {
		return nil, fmt.Errorf("failed to set controller reference for placement: %w", err)
	}

	if err := r.Create(ctx, placement); err != nil {
		return nil, fmt.Errorf("failed to create Placement: %w", err)
	}

	return placement, nil
}

func (r *P2CodeSchedulingManifestReconciler) extractFailedCondition(ctx context.Context, placementCondition metav1.Condition, managedClusterSetName string) (*metav1.Condition, error) {
	condition := metav1.Condition{Type: schedulingFailed, Status: metav1.ConditionTrue, Reason: placementCondition.Reason, Message: placementCondition.Message}

	if placementCondition.Reason == "NoManagedClusterMatched" {
		condition.Reason = "SchedulingAnnotationsUnsatisfied"
		condition.Message = fmt.Sprintf("Unable to find a suitable location for the workload as there are no managed clusters in the %s managed cluster set that satisfy the annotations requested", managedClusterSetName)
	}

	if placementCondition.Reason == "NoIntersection" {
		exists, err := r.doesManagedClusterSetExist(ctx, managedClusterSetName)

		if err != nil {
			return nil, fmt.Errorf("%w", err)
		}

		if exists {
			condition.Reason = "InaccessibleManagedClusterSet"
			condition.Message = fmt.Sprintf("The scheduler is not authorized to access the %s managed cluster set", managedClusterSetName)
		} else {
			condition.Reason = "InvalidTarget"
			condition.Message = fmt.Sprintf("Cannot find a managed cluster set with the name %s", managedClusterSetName)
		}
	}

	if placementCondition.Reason == "AllManagedClusterSetsEmpty" {
		condition.Reason = "InvalidTarget"
		condition.Message = fmt.Sprintf("The managed cluster set selected (%s) is empty", managedClusterSetName)
	}

	if placementCondition.Reason == "NoManagedClusterSetBindings" {
		condition.Reason = "InaccessibleManagedClusterSet"
		condition.Message = "The scheduler is not authorized to access any managed cluster sets"
	}

	return &condition, nil
}

func (r *P2CodeSchedulingManifestReconciler) generateManifestWorkForBundle(name string, namespace string, ownerLabel string, bundeledResources ResourceSet) (workv1.ManifestWork, error) {
	manifestList := []workv1.Manifest{}

	for _, resource := range bundeledResources {
		// Ensure a namespace is defined for the resource
		// Unless the resource is not namespaced eg namespace
		if resource.metadata.namespace == "" && resource.metadata.groupVersionKind.Kind != "Namespace" {
			errorMessage := fmt.Sprintf("invalid manifest, no namespace specified for %s %s", resource.metadata.name, resource.metadata.groupVersionKind.Kind)
			return workv1.ManifestWork{}, &MisconfiguredManifestError{errorMessage}
		}

		m := workv1.Manifest{
			RawExtension: resource.manifest,
		}
		manifestList = append(manifestList, m)
	}

	manifestWork := workv1.ManifestWork{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				ownershipLabel: ownerLabel,
			},
		},
		Spec: workv1.ManifestWorkSpec{
			Workload: workv1.ManifestsTemplate{
				Manifests: manifestList,
			},
		},
	}

	return manifestWork, nil
}

func (r *P2CodeSchedulingManifestReconciler) validateManifestWorkApplied(manifestWork workv1.ManifestWork) error {
	if len(manifestWork.Status.ResourceStatus.Manifests) < 1 {
		errorMessage := fmt.Sprintf("ResourceStatus for %s ManifestWork is empty", manifestWork.Name)
		return &ManifestWorkNotReadyError{errorMessage}
	}

	for _, manifestStatus := range manifestWork.Status.ResourceStatus.Manifests {
		for _, condition := range manifestStatus.Conditions {
			if (condition.Type == "Applied" && condition.Status == metav1.ConditionFalse) || (condition.Type == "Available" && condition.Status == metav1.ConditionFalse) {
				return &ManifestWorkFailedError{condition.Message}
			}
		}
	}

	return nil
}

func (r *P2CodeSchedulingManifestReconciler) getSelectedCluster(ctx context.Context, placement clusterv1beta1.Placement) (string, error) {
	placementDecisionName := placement.Status.DecisionGroups[0].Decisions[0]
	placementDecision := &clusterv1beta1.PlacementDecision{}
	err := r.Get(ctx, types.NamespacedName{Name: placementDecisionName, Namespace: P2CodeSchedulerNamespace}, placementDecision)
	if err != nil {
		return "", fmt.Errorf("%w", err)
	}

	return placementDecision.Status.Decisions[0].ClusterName, nil
}

func (r *P2CodeSchedulingManifestReconciler) getSchedulingDecisions(p2CodeSchedulingManifest *schedulingv1alpha1.P2CodeSchedulingManifest) []schedulingv1alpha1.SchedulingDecision {
	schedulingDecisions := []schedulingv1alpha1.SchedulingDecision{}
	for _, bundle := range r.Bundles[p2CodeSchedulingManifest.Name] {
		decision := schedulingv1alpha1.SchedulingDecision{WorkloadName: bundle.name, ClusterSelected: bundle.clusterName}
		schedulingDecisions = append(schedulingDecisions, decision)
	}

	return schedulingDecisions
}

// nolint:cyclop // not to concerned about cognitive complexity (brainfreeze)
func extractPodSpec(workload Resource) (*corev1.PodSpec, error) {
	switch kind := workload.metadata.groupVersionKind.Kind; kind {
	case "Pod":
		pod := &corev1.Pod{}
		if err := json.Unmarshal(workload.manifest.Raw, pod); err != nil {
			return nil, fmt.Errorf("%w", err)
		}
		return &pod.Spec, nil
	case "Deployment":
		deployment := &appsv1.Deployment{}
		if err := json.Unmarshal(workload.manifest.Raw, deployment); err != nil {
			return nil, fmt.Errorf("%w", err)
		}
		return &deployment.Spec.Template.Spec, nil
	case "StatefulSet":
		statefulset := &appsv1.StatefulSet{}
		if err := json.Unmarshal(workload.manifest.Raw, statefulset); err != nil {
			return nil, fmt.Errorf("%w", err)
		}
		return &statefulset.Spec.Template.Spec, nil
	case "DaemonSet":
		daemonset := &appsv1.DaemonSet{}
		if err := json.Unmarshal(workload.manifest.Raw, daemonset); err != nil {
			return nil, fmt.Errorf("%w", err)
		}
		return &daemonset.Spec.Template.Spec, nil
	case "Job":
		job := &batchv1.Job{}
		if err := json.Unmarshal(workload.manifest.Raw, job); err != nil {
			return nil, fmt.Errorf("%w", err)
		}
		return &job.Spec.Template.Spec, nil
	case "CronJob":
		cronJob := &batchv1.CronJob{}
		if err := json.Unmarshal(workload.manifest.Raw, cronJob); err != nil {
			return nil, fmt.Errorf("%w", err)
		}
		return &cronJob.Spec.JobTemplate.Spec.Template.Spec, nil
	default:
		return nil, fmt.Errorf("unable to extract the pod spec for workload %s of type %s", workload.metadata.name, workload.metadata.groupVersionKind.Kind)
	}
}

// SetupWithManager sets up the controller with the Manager.
// nolint:wrapcheck
func (r *P2CodeSchedulingManifestReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&schedulingv1alpha1.P2CodeSchedulingManifest{}).
		Owns(&clusterv1beta1.Placement{}).
		Owns(&workv1.ManifestWork{}).
		// Don't trigger the reconciler if an update doesnt change the metadata.generation field of the object being reconciled
		// This means that an update event for the object's status section will not trigger the reconciler
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		Complete(r)
}
