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
	"slices"
	"time"

	"github.com/iancoleman/strcase"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	utils "github.com/PoolPooer/p2code-scheduler/utils"

	schedulingv1alpha1 "github.com/PoolPooer/p2code-scheduler/api/v1alpha1"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	clusterv1beta1 "open-cluster-management.io/api/cluster/v1beta1"
	clusterv1beta2 "open-cluster-management.io/api/cluster/v1beta2"
	workv1 "open-cluster-management.io/api/work/v1"
)

const finalizer = "scheduling.p2code.eu/finalizer"
const ownershipLabel = "scheduling.p2code.eu/owner"
const P2CodeSchedulerNamespace = "p2code-scheduler-system"

// Status conditions corresponding to the state of the P2CodeSchedulingManifest
const (
	schedulingInProgress = "SchedulingInProgress"
	schedulingSuccessful = "SchedulingSuccessful"
	schedulingFailed     = "SchedulingFailed"
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

type ManifestMetadata struct {
	name             string
	namespace        string
	groupVersionKind schema.GroupVersionKind
	labels           map[string]string
}

type Resource struct {
	metadata                    ManifestMetadata
	p2codeSchedulingAnnotations []string
	manifest                    runtime.RawExtension
}

type ResourceSet []*Resource

type Bundle struct {
	name                string
	resources           ResourceSet
	externalConnections []string
	clusterName         string
}

type NoNamespaceError struct {
	message string
}

type ManifestWorkFailedError struct {
	message string
}

type ManifestWorkNotReady struct {
	message string
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
		log.Error(err, "Failed to get P2CodeSchedulingManifest")
		return ctrl.Result{}, err
	}

	// If the status is empty set the status as scheduling in progress
	if len(p2CodeSchedulingManifest.Status.Conditions) == 0 {
		meta.SetStatusCondition(&p2CodeSchedulingManifest.Status.Conditions, metav1.Condition{Type: schedulingInProgress, Status: metav1.ConditionTrue, Reason: schedulingInProgress, Message: "Analysing scheduling requirements"})
		p2CodeSchedulingManifest.Status.Decisions = []schedulingv1alpha1.SchedulingDecision{}
		if err := r.Status().Update(ctx, p2CodeSchedulingManifest); err != nil {
			log.Error(err, "Failed to update P2CodeSchedulingManifest status")
			return ctrl.Result{}, err
		}
	}

	// Validate that the P2CodeSchedulingManifest instance is in the correct namespace
	if req.Namespace != P2CodeSchedulerNamespace {
		errorTitle := "incorrect namespace"
		errorMessage := "Ignoring resource as it is not in the p2code-scheduler-system namespace"

		err := fmt.Errorf("%s", errorTitle)
		log.Error(err, errorMessage)

		meta.SetStatusCondition(&p2CodeSchedulingManifest.Status.Conditions, metav1.Condition{Type: misconfigured, Status: metav1.ConditionTrue, Reason: strcase.ToCamel(errorTitle), Message: errorMessage})
		p2CodeSchedulingManifest.Status.Decisions = []schedulingv1alpha1.SchedulingDecision{}
		if err := r.Status().Update(ctx, p2CodeSchedulingManifest); err != nil {
			log.Error(err, "Failed to update P2CodeSchedulingManifest status")
			return ctrl.Result{}, err
		}

		return ctrl.Result{}, nil
	}

	// Ensure P2CodeSchedulingManifest instance has a finalizer
	if !controllerutil.ContainsFinalizer(p2CodeSchedulingManifest, finalizer) {
		// Refetch P2CodeSchedulingManifest instance to get the latest state of the resource
		if err := r.Get(ctx, req.NamespacedName, p2CodeSchedulingManifest); err != nil {
			log.Error(err, "Failed to refetch P2CodeSchedulingManifest")
			return ctrl.Result{}, err
		}

		if ok := controllerutil.AddFinalizer(p2CodeSchedulingManifest, finalizer); !ok {
			log.Error(err, "Failed to add finalizer to P2CodeSchedulingManifest instance")
			return ctrl.Result{Requeue: true}, nil
		}

		if err := r.Update(ctx, p2CodeSchedulingManifest); err != nil {
			log.Error(err, "Failed to update P2CodeSchedulingManifest instance with finalizer")
			return ctrl.Result{}, err
		}

		log.Info("Finalizer added to P2CodeSchedulingManifest")
	}

	// Check if P2CodeSchedulingManifest instance is marked for deletion
	if p2CodeSchedulingManifest.GetDeletionTimestamp() != nil {
		if controllerutil.ContainsFinalizer(p2CodeSchedulingManifest, finalizer) {
			message := "Performing finalizing operations before deleting P2CodeSchedulingManifest"
			log.Info(message)

			meta.SetStatusCondition(&p2CodeSchedulingManifest.Status.Conditions, metav1.Condition{Type: finalizing, Status: metav1.ConditionTrue, Reason: "Finalizing", Message: message})
			p2CodeSchedulingManifest.Status.Decisions = []schedulingv1alpha1.SchedulingDecision{}
			if err := r.Status().Update(ctx, p2CodeSchedulingManifest); err != nil {
				log.Error(err, "Failed to update P2CodeSchedulingManifest status")
				return ctrl.Result{}, err
			}

			// Deleting ManifestWork resources associated with this P2CodeSchedulingManifest instance
			// Placements and PlacementDecisions are automatically cleaned up as this instance is set as the owner reference for those resources
			if err := r.deleteOwnedManifestWorkList(p2CodeSchedulingManifest.Name); err != nil {
				log.Error(err, "Failed to perform clean up operations on instance before deleting")
				return ctrl.Result{}, err
			}

			r.deleteBundles(p2CodeSchedulingManifest.Name)

			// Fetch updated P2CodeSchedulingManifest instance
			if err := r.Get(ctx, req.NamespacedName, p2CodeSchedulingManifest); err != nil {
				log.Error(err, "Failed to re-fetch P2CodeSchedulingManifest")
				return ctrl.Result{}, err
			}

			message = "Removing finalizer to allow instance to be deleted"
			log.Info(message)

			meta.SetStatusCondition(&p2CodeSchedulingManifest.Status.Conditions, metav1.Condition{Type: finalizing, Status: metav1.ConditionTrue, Reason: finalizing, Message: message})
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

	// Ensure all annotations specified are supported by the scheduler
	ok, err := validateAnnotationsSupported(p2CodeSchedulingManifest)
	if !ok {
		errorMessage := "Unsupported annotations found"
		log.Error(err, errorMessage)

		meta.SetStatusCondition(&p2CodeSchedulingManifest.Status.Conditions, metav1.Condition{Type: misconfigured, Status: metav1.ConditionTrue, Reason: "UnsupportedAnnotation", Message: errorMessage + ", " + err.Error()})
		p2CodeSchedulingManifest.Status.Decisions = []schedulingv1alpha1.SchedulingDecision{}
		if err := r.Status().Update(ctx, p2CodeSchedulingManifest); err != nil {
			log.Error(err, "Failed to update P2CodeSchedulingManifest status")
			return ctrl.Result{}, err
		}

		return ctrl.Result{}, nil
	}

	// Extract the target cluster set and optional target cluster from the P2CodeSchedulingManifest instance
	targetClusterSet, targetCluster, err := utils.ExtractTarget(p2CodeSchedulingManifest.Spec.GlobalAnnotations)
	if err != nil {
		errorMessage := "Target information missing from the scheduling manifest"
		log.Error(err, errorMessage)

		meta.SetStatusCondition(&p2CodeSchedulingManifest.Status.Conditions, metav1.Condition{Type: misconfigured, Status: metav1.ConditionTrue, Reason: "MissingTarget", Message: errorMessage + ", " + err.Error()})
		p2CodeSchedulingManifest.Status.Decisions = []schedulingv1alpha1.SchedulingDecision{}
		if err := r.Status().Update(ctx, p2CodeSchedulingManifest); err != nil {
			log.Error(err, "Failed to update P2CodeSchedulingManifest status")
			return ctrl.Result{}, err
		}

		return ctrl.Result{}, nil
	}

	// Validate that the cluster set exists
	exists, err := r.doesManagedClusterSetExist(targetClusterSet)
	if err != nil {
		log.Error(err, "An error occurred while validating the managed cluster set")
		return ctrl.Result{}, err
	}

	if !exists {
		message := fmt.Sprintf("Cannot find a managed cluster set with the name %s", targetClusterSet)
		condition := metav1.Condition{Type: schedulingFailed, Status: metav1.ConditionTrue, Reason: "InvalidTarget", Message: message}
		meta.SetStatusCondition(&p2CodeSchedulingManifest.Status.Conditions, condition)
		p2CodeSchedulingManifest.Status.Decisions = []schedulingv1alpha1.SchedulingDecision{}
		if err := r.Status().Update(ctx, p2CodeSchedulingManifest); err != nil {
			log.Error(err, "Failed to update P2CodeSchedulingManifest status")
			return ctrl.Result{}, err
		}

		log.Info(message)
		return ctrl.Result{}, nil
	}

	// Ensure the cluster set is not empty
	empty, err := r.isClusterSetEmpty(targetClusterSet)
	if err != nil {
		log.Error(err, "An error occurred while examining the cluster set")
		return ctrl.Result{}, err
	}

	if empty {
		message := fmt.Sprintf("The managed cluster set selected (%s) is empty", targetClusterSet)
		condition := metav1.Condition{Type: schedulingFailed, Status: metav1.ConditionTrue, Reason: "InvalidTarget", Message: message}
		meta.SetStatusCondition(&p2CodeSchedulingManifest.Status.Conditions, condition)
		p2CodeSchedulingManifest.Status.Decisions = []schedulingv1alpha1.SchedulingDecision{}
		if err := r.Status().Update(ctx, p2CodeSchedulingManifest); err != nil {
			log.Error(err, "Failed to update P2CodeSchedulingManifest status")
			return ctrl.Result{}, err
		}

		log.Info(message)
		return ctrl.Result{}, nil
	}

	// Ensure the cluster set specified is bound to the controller namespace
	bound, err := r.isClusterSetBound(targetClusterSet)
	if err != nil {
		log.Error(err, "An error occurred while examining the cluster set bindings")
		return ctrl.Result{}, err
	}

	if !bound {
		message := fmt.Sprintf("The scheduler is not authorized to access the %s managed cluster set", targetClusterSet)
		condition := metav1.Condition{Type: schedulingFailed, Status: metav1.ConditionTrue, Reason: "InaccessibleManagedClusterSet", Message: message}
		meta.SetStatusCondition(&p2CodeSchedulingManifest.Status.Conditions, condition)
		p2CodeSchedulingManifest.Status.Decisions = []schedulingv1alpha1.SchedulingDecision{}
		if err := r.Status().Update(ctx, p2CodeSchedulingManifest); err != nil {
			log.Error(err, "Failed to update P2CodeSchedulingManifest status")
			return ctrl.Result{}, err
		}

		log.Info(message)
		return ctrl.Result{}, nil
	}

	// If a target cluster is specified all manifests are bundled together and sent to the given cluster
	// Otherwise the scheduler identifies a suitable cluster for each workload in the P2CodeSchedulingManifest and bundles the workload with its ancillary resources
	if targetCluster != "" {
		// Validate that the cluster set specified contains the target cluster
		labelSelector, err := labels.Parse(fmt.Sprintf("cluster.open-cluster-management.io/clusterset=%s", targetClusterSet))
		if err != nil {
			log.Error(err, "An occurred error creating the label selector")
			return ctrl.Result{}, err
		}

		listOptions := client.ListOptions{
			LabelSelector: labelSelector,
		}

		managedClusters := &clusterv1.ManagedClusterList{}
		err = r.List(ctx, managedClusters, &listOptions)
		if err != nil {
			log.Error(err, "An error occurred while validating the target cluster's cluster set membership")
			return ctrl.Result{}, err
		}

		if len(managedClusters.Items) < 1 {
			message := fmt.Sprintf("Cannot find a managed cluster with the name %s in the %s managed cluster set", targetCluster, targetClusterSet)
			condition := metav1.Condition{Type: schedulingFailed, Status: metav1.ConditionTrue, Reason: "InvalidTarget", Message: message}
			meta.SetStatusCondition(&p2CodeSchedulingManifest.Status.Conditions, condition)
			p2CodeSchedulingManifest.Status.Decisions = []schedulingv1alpha1.SchedulingDecision{}
			if err := r.Status().Update(ctx, p2CodeSchedulingManifest); err != nil {
				log.Error(err, "Failed to update P2CodeSchedulingManifest status")
				return ctrl.Result{}, err
			}

			log.Info(message)
			return ctrl.Result{}, nil
		}

		// Extract resources from the P2CodeSchedulingManifest instance to populate the bundle
		resources, err := bulkConvertToResource(p2CodeSchedulingManifest.Spec.Manifests)
		if err != nil {
			log.Error(err, "Failed to process manifests to be scheduled")
			return ctrl.Result{}, err
		}

		// Create a bundle if necessary and set the clusterName to the targetCluster specified
		_, err = r.getBundle(p2CodeSchedulingManifest.Name, p2CodeSchedulingManifest.Name)
		if err != nil {
			bundle := &Bundle{name: p2CodeSchedulingManifest.Name, resources: resources, clusterName: targetCluster}
			r.Bundles[p2CodeSchedulingManifest.Name] = append(r.Bundles[p2CodeSchedulingManifest.Name], bundle)
		}

	} else {
		err := r.identifyTargetCluster(ctx, p2CodeSchedulingManifest)
		if err != nil {
			log.Error(err, "Failed to identify a target cluster")
			return ctrl.Result{}, err
		}

		// Fetch updated P2CodeSchedulingManifest instance
		if err := r.Get(ctx, req.NamespacedName, p2CodeSchedulingManifest); err != nil {
			log.Error(err, "Failed to re-fetch P2CodeSchedulingManifest")
			return ctrl.Result{}, err
		}

		// Check latest status condition to determine if reconciliation is complete
		latestCondition := p2CodeSchedulingManifest.Status.Conditions[len(p2CodeSchedulingManifest.Status.Conditions)-1]
		if latestCondition.Type != schedulingInProgress {
			infoMessage := fmt.Sprintf("Reconciliation complete: %s", latestCondition.Message)
			log.Info(infoMessage)
			return ctrl.Result{}, nil
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
			var noNamespaceErr *NoNamespaceError
			if errors.As(err, &noNamespaceErr) {
				errorMessage := "Namespace omitted"
				log.Error(err, errorMessage)

				meta.SetStatusCondition(&p2CodeSchedulingManifest.Status.Conditions, metav1.Condition{Type: misconfigured, Status: metav1.ConditionTrue, Reason: "MisconfiguredManifest", Message: errorMessage + ", " + err.Error()})
				p2CodeSchedulingManifest.Status.Decisions = []schedulingv1alpha1.SchedulingDecision{}
				if err := r.Status().Update(ctx, p2CodeSchedulingManifest); err != nil {
					log.Error(err, "Failed to update P2CodeSchedulingManifest status")
					return ctrl.Result{}, err
				}

				return ctrl.Result{}, nil

			} else if err != nil {
				log.Error(err, "Failed to generate ManifestWork")
				return ctrl.Result{}, err
			}

			manifestWorks = append(manifestWorks, newManifestWork)
		}
	}

	// Ensure there are no orphaned manifests
	// Orphaned manifests can arise if there are ancillary services that are not referenced by a workload resource and are therefore not added to a bundle
	// If there are more manifests in the CR than the count of manifests across all ManifestWorks this indicates that some manifests are unaccounted for
	// It is unlikely that the values are equal since an ancillary manifest can be included in many ManifestWorks
	if len(p2CodeSchedulingManifest.Spec.Manifests) > placedManifests {
		errorTitle := "orphaned manifest"
		errorMessage := "Orphaned manifests found: Ensure all ancillary resources (services, configmaps, secrets, etc) are referenced by a workload resource"

		err := fmt.Errorf("%s", errorTitle)
		log.Error(err, errorMessage)

		meta.SetStatusCondition(&p2CodeSchedulingManifest.Status.Conditions, metav1.Condition{Type: misconfigured, Status: metav1.ConditionTrue, Reason: strcase.ToCamel(errorTitle), Message: errorMessage})
		p2CodeSchedulingManifest.Status.Decisions = []schedulingv1alpha1.SchedulingDecision{}
		if err := r.Status().Update(ctx, p2CodeSchedulingManifest); err != nil {
			log.Error(err, "Failed to update P2CodeSchedulingManifest status")
			return ctrl.Result{}, err
		}

		return ctrl.Result{}, nil
	}

	// Update status with scheduling decisions
	message := "A suitable cluster has been identified for each workload: Creating the corresponding ManifestWork to schedule the workload to the identified cluster"
	log.Info(message)

	schedulingDecisions := []schedulingv1alpha1.SchedulingDecision{}
	for _, bundle := range r.Bundles[p2CodeSchedulingManifest.Name] {
		decision := schedulingv1alpha1.SchedulingDecision{WorkloadName: bundle.name, ClusterSelected: bundle.clusterName}
		schedulingDecisions = append(schedulingDecisions, decision)
	}

	// Refetch P2CodeSchedulingManifest instance before updating the status
	if err := r.Get(ctx, req.NamespacedName, p2CodeSchedulingManifest); err != nil {
		log.Error(err, "Failed to refetch P2CodeSchedulingManifest")
		return ctrl.Result{}, err
	}

	meta.SetStatusCondition(&p2CodeSchedulingManifest.Status.Conditions, metav1.Condition{Type: schedulingInProgress, Status: metav1.ConditionTrue, Reason: "ManifestWorkReady", Message: message})
	p2CodeSchedulingManifest.Status.Decisions = schedulingDecisions
	if err := r.Status().Update(ctx, p2CodeSchedulingManifest); err != nil {
		log.Error(err, "Failed to update P2CodeSchedulingManifest status")
		return ctrl.Result{}, err
	}

	// Create ManifestWorks
	// TODO Do need to delete manifest works already deployed if one fails to create - use deleteOwnedManifestWorkList func
	for _, manifestWork := range manifestWorks {
		if err = r.Create(ctx, &manifestWork); err != nil {
			log.Error(err, "Failed to create ManifestWork")
			return ctrl.Result{}, err
		}
	}

	// Fetch list of applied ManifestWorks owned by this P2CodeSchedulingManifest instance
	manifestWorkList, err := r.getOwnedManifestWorkList(p2CodeSchedulingManifest.Name)
	if err != nil {
		log.Error(err, "Failed to fetch list of ManifestWorks owned by the P2CodeSchedulingManifest instance")
		return ctrl.Result{}, err
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
		var manifestWorkNotReady *ManifestWorkNotReady
		if errors.As(err, &manifestWorkFailedErr) {
			log.Error(err, "Failed to apply ManifestWork")

			// Refetch P2CodeSchedulingManifest instance before updating the status
			if err := r.Get(ctx, req.NamespacedName, p2CodeSchedulingManifest); err != nil {
				log.Error(err, "Failed to refetch P2CodeSchedulingManifest")
				return ctrl.Result{}, err
			}

			meta.SetStatusCondition(&p2CodeSchedulingManifest.Status.Conditions, metav1.Condition{Type: schedulingFailed, Status: metav1.ConditionTrue, Reason: "ManifestWorkFailed", Message: err.Error()})
			p2CodeSchedulingManifest.Status.Decisions = schedulingDecisions
			if err := r.Status().Update(ctx, p2CodeSchedulingManifest); err != nil {
				log.Error(err, "Failed to update P2CodeSchedulingManifest status")
				return ctrl.Result{}, err
			}

			return ctrl.Result{}, nil

		} else if errors.As(err, &manifestWorkNotReady) {
			message := fmt.Sprintf("Waiting for %s ManifestWork to be ready: %s, requeuing", manifestWork.Name, err.Error())
			log.Info(message)
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		} else if err != nil {
			log.Error(err, "Error occurred while validating the state of the ManifestWork applied")
			return ctrl.Result{}, err
		}
	}

	message = "All workloads have been successfully scheduled to a suitable cluster"
	log.Info(message)

	// Refetch P2CodeSchedulingManifest instance before updating the status
	if err := r.Get(ctx, req.NamespacedName, p2CodeSchedulingManifest); err != nil {
		log.Error(err, "Failed to refetch P2CodeSchedulingManifest")
		return ctrl.Result{}, err
	}

	meta.SetStatusCondition(&p2CodeSchedulingManifest.Status.Conditions, metav1.Condition{Type: schedulingSuccessful, Status: metav1.ConditionTrue, Reason: schedulingSuccessful, Message: message})
	p2CodeSchedulingManifest.Status.Decisions = schedulingDecisions
	if err := r.Status().Update(ctx, p2CodeSchedulingManifest); err != nil {
		log.Error(err, "Failed to update P2CodeSchedulingManifest status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// TODO might as well analyse the resource requests in here when already extracted add a field for resource requests cpu etc to the bundle
func analyseWorkload(workload *Resource, ancillaryResources ResourceSet) (ResourceSet, []string, error) {
	resources := ResourceSet{}
	externalConnections := []string{}

	if workload.metadata.namespace == "" {
		errorMessage := fmt.Sprintf("no namespace defined for the workload %s", workload.metadata.name)
		return ResourceSet{}, []string{}, &NoNamespaceError{errorMessage}
	}

	if workload.metadata.namespace != "default" {
		namespaceResource, err := ancillaryResources.Find(workload.metadata.namespace, "Namespace")
		if err != nil {
			return ResourceSet{}, []string{}, err
		}

		resources.Add(namespaceResource)
	}

	podSpec, err := extractPodSpec(*workload)
	if err != nil {
		return ResourceSet{}, []string{}, err
	}

	// TODO suport later imagePullSecrets
	// Need to consider nodeSelector, tolerations ??

	// Later could support other types and check for aws and azure types
	// look at storage classes
	if len(podSpec.Volumes) > 0 {
		for _, volume := range podSpec.Volumes {
			if volume.PersistentVolumeClaim != nil {
				pvcResource, err := ancillaryResources.Find(volume.PersistentVolumeClaim.ClaimName, "PersistentVolumeClaim")
				if err != nil {
					return ResourceSet{}, []string{}, err
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
					return ResourceSet{}, []string{}, err
				}

				resources.Add(cmResource)
			}

			if volume.Secret != nil {
				secretResource, err := ancillaryResources.Find(volume.Secret.SecretName, "Secret")
				if err != nil {
					return ResourceSet{}, []string{}, err
				}

				resources.Add(secretResource)
			}
		}
	}

	if podSpec.ServiceAccountName != "" {
		saResource, err := ancillaryResources.Find(podSpec.ServiceAccountName, "ServiceAccount")
		if err != nil {
			return ResourceSet{}, []string{}, err
		}

		resources.Add(saResource)
	}

	// Examine Containers and InitContainers for ancillary resources
	containers := podSpec.Containers
	containers = append(containers, podSpec.InitContainers...)

	// TODO examine resource requests for container
	for _, container := range containers {
		if len(container.EnvFrom) > 0 {
			for _, envSource := range container.EnvFrom {
				if envSource.ConfigMapRef != nil {
					cmResource, err := ancillaryResources.Find(envSource.ConfigMapRef.Name, "ConfigMap")
					if err != nil {
						return ResourceSet{}, []string{}, err
					}

					resources.Add(cmResource)
				}

				if envSource.SecretRef != nil {
					secretResource, err := ancillaryResources.Find(envSource.SecretRef.Name, "Secret")
					if err != nil {
						return ResourceSet{}, []string{}, err
					}

					resources.Add(secretResource)
				}
			}
		}

		if len(container.Env) > 0 {
			for _, envVar := range container.Env {
				if envVar.ValueFrom != nil {
					if envVar.ValueFrom.ConfigMapKeyRef != nil {
						cmResource, err := ancillaryResources.Find(envVar.ValueFrom.ConfigMapKeyRef.Name, "ConfigMap")
						if err != nil {
							return ResourceSet{}, []string{}, err
						}

						resources.Add(cmResource)
					}

					if envVar.ValueFrom.SecretKeyRef != nil {
						secretResource, err := ancillaryResources.Find(envVar.ValueFrom.SecretKeyRef.Name, "Secret")
						if err != nil {
							return ResourceSet{}, []string{}, err
						}

						resources.Add(secretResource)
					}
				}
			}
		}
	}

	// Look into securityContextProfile under container and pod for later version

	// TODO test this case
	// Add services to the bundle
	if workload.metadata.groupVersionKind.Kind == "StatefulSet" {
		// If the workload is a StatefulSet the associated service can be found in the ServiceName field of its spec
		// volumeClaimTemplate is a list of pvc, not reference to pvc, look at storage classes
		statefulset := &appsv1.StatefulSet{}
		if err := json.Unmarshal(workload.manifest.Raw, statefulset); err != nil {
			return ResourceSet{}, []string{}, err
		}

		svcResource, err := ancillaryResources.Find(statefulset.Spec.ServiceName, "Service")
		if err != nil {
			return ResourceSet{}, []string{}, err
		}

		resources.Add(svcResource)
	} else {
		// Get a list of all services and check if the service selector matches the labels on the workload
		services := ancillaryResources.FilterByKind("Service")
		for _, service := range services {
			svc := &corev1.Service{}
			if err := json.Unmarshal(service.manifest.Raw, svc); err != nil {
				return ResourceSet{}, []string{}, err
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

func bulkConvertToResource(manifests []runtime.RawExtension) (ResourceSet, error) {
	resources := ResourceSet{}
	for _, manifest := range manifests {
		object := &unstructured.Unstructured{}
		if err := object.UnmarshalJSON(manifest.Raw); err != nil {
			return ResourceSet{}, err
		}

		metadata := ManifestMetadata{name: object.GetName(), namespace: object.GetNamespace(), groupVersionKind: object.GetObjectKind().GroupVersionKind(), labels: object.GetLabels()}
		resources.Add(&Resource{metadata: metadata, manifest: manifest})
	}
	return resources, nil
}

func (r *P2CodeSchedulingManifestReconciler) identifyTargetCluster(ctx context.Context, p2CodeSchedulingManifest *schedulingv1alpha1.P2CodeSchedulingManifest) error {
	log := log.FromContext(ctx)

	placementBundleMap := make(map[string]*Bundle)

	// Get target managed cluster set
	managedClusterSetName, _, err := utils.ExtractTarget(p2CodeSchedulingManifest.Spec.GlobalAnnotations)
	if err != nil {
		log.Error(err, "Failed to extract target information")
		return err
	}

	// Convert p2CodeSchedulingManifest.Spec.Manifests to Resources for easier manipulation
	resources, err := bulkConvertToResource(p2CodeSchedulingManifest.Spec.Manifests)
	if err != nil {
		log.Error(err, "Failed to process manifests to be scheduled")
		return err
	}

	commonPlacementRules := utils.ExtractPlacementRules(p2CodeSchedulingManifest.Spec.GlobalAnnotations)

	// If no filter annotations are specified all manifests are scheduled to a random cluster within the target managed cluster set
	// Create an empty placement and allow the Placement API to decide on a random cluster from the target managed cluster set
	// Later take into account the resource requests of each workload
	if len(commonPlacementRules) == 0 && len(p2CodeSchedulingManifest.Spec.WorkloadAnnotations) == 0 {
		placementName := fmt.Sprintf("%s-default", p2CodeSchedulingManifest.Name)

		// Check if a default bundle already exists
		bundle, err := r.getBundle("default", p2CodeSchedulingManifest.Name)
		if err != nil {
			log.Info("Creating default placement for all manifests")
			bundle = &Bundle{name: "default", resources: resources}
			r.Bundles[p2CodeSchedulingManifest.Name] = append(r.Bundles[p2CodeSchedulingManifest.Name], bundle)
			if err := r.createPlacement(placementName, managedClusterSetName, []metav1.LabelSelectorRequirement{}, p2CodeSchedulingManifest); err != nil {
				log.Error(err, "Failed to create placement")
				return err
			}
		}

		placementBundleMap[placementName] = bundle
	}

	// If no filter annotations are specified at the workload level but filter annotations are provided at the global level all manifests are scheduled to a cluster matching the global filter annotations
	if len(commonPlacementRules) > 0 && len(p2CodeSchedulingManifest.Spec.WorkloadAnnotations) == 0 {
		placementName := fmt.Sprintf("%s-global", p2CodeSchedulingManifest.Name)

		// Check if a global bundle already exists
		bundle, err := r.getBundle("global", p2CodeSchedulingManifest.Name)
		if err != nil {
			log.Info("Creating global placement for all manifests")
			bundle = &Bundle{name: "global", resources: resources}
			r.Bundles[p2CodeSchedulingManifest.Name] = append(r.Bundles[p2CodeSchedulingManifest.Name], bundle)
			if err := r.createPlacement(placementName, managedClusterSetName, commonPlacementRules, p2CodeSchedulingManifest); err != nil {
				log.Error(err, "Failed to create placement")
				return err
			}
		}

		placementBundleMap[placementName] = bundle
	}

	// Check if any workload filter annotations are specified
	// Bundle workload and its ancillary resources with the filter annotation
	if len(p2CodeSchedulingManifest.Spec.WorkloadAnnotations) > 0 {
		workloads, ancillaryResources := resources.Categorise()

		// Ensure workloadAnnotations refer to a valid workload
		if err := assignWorkloadAnnotations(workloads, p2CodeSchedulingManifest.Spec.WorkloadAnnotations); err != nil {
			errorMessage := "Invalid workload annotation provided"
			log.Error(err, errorMessage)
			meta.SetStatusCondition(&p2CodeSchedulingManifest.Status.Conditions, metav1.Condition{Type: misconfigured, Status: metav1.ConditionTrue, Reason: "InvalidWorkloadAnnotation", Message: errorMessage + ", " + err.Error()})
			p2CodeSchedulingManifest.Status.Decisions = []schedulingv1alpha1.SchedulingDecision{}
			if err := r.Status().Update(ctx, p2CodeSchedulingManifest); err != nil {
				log.Error(err, "Failed to update P2CodeSchedulingManifest status")
				return err
			}
		}

		for _, workload := range workloads {
			placementName := fmt.Sprintf("%s-%s-bundle", p2CodeSchedulingManifest.Name, workload.metadata.name)

			// Check if a bundle already exists for the workload
			// Create a bundle for the workload if needed and find its ancillary resources
			bundle, err := r.getBundle(workload.metadata.name, p2CodeSchedulingManifest.Name)
			if err != nil {
				message := fmt.Sprintf("Analysing workload %s for its ancillary resources", workload.metadata.name)
				log.Info(message)
				workloadAncillaryResources, externalConnections, err := analyseWorkload(workload, ancillaryResources)
				var noNamespaceErr *NoNamespaceError
				if errors.As(err, &noNamespaceErr) {
					errorMessage := "Namespace omitted"
					log.Error(err, errorMessage)

					meta.SetStatusCondition(&p2CodeSchedulingManifest.Status.Conditions, metav1.Condition{Type: misconfigured, Status: metav1.ConditionTrue, Reason: "MisconfiguredManifest", Message: errorMessage + ", " + err.Error()})
					p2CodeSchedulingManifest.Status.Decisions = []schedulingv1alpha1.SchedulingDecision{}
					if err := r.Status().Update(ctx, p2CodeSchedulingManifest); err != nil {
						log.Error(err, "Failed to update P2CodeSchedulingManifest status")
						return err
					}
				} else if err != nil {
					log.Error(err, "Error occurred while analysing workload for ancillary resources")
					return err
				}

				bundleResources := ResourceSet{}
				bundleResources = append(bundleResources, workload)
				bundleResources = append(bundleResources, workloadAncillaryResources...)
				bundle = &Bundle{name: workload.metadata.name, resources: bundleResources, externalConnections: externalConnections}
				r.Bundles[p2CodeSchedulingManifest.Name] = append(r.Bundles[p2CodeSchedulingManifest.Name], bundle)
			}

			additionalPlacementRules := utils.ExtractPlacementRules(workload.p2codeSchedulingAnnotations)
			placementRules := slices.Concat(commonPlacementRules, additionalPlacementRules)
			// TODO calculateWorkloadResourceRequests(workload)

			if err := r.createPlacement(placementName, managedClusterSetName, placementRules, p2CodeSchedulingManifest); err != nil {
				log.Error(err, "Failed to create placement")
				return err
			}

			placementBundleMap[placementName] = bundle
		}
	}

	for placementName, bundle := range placementBundleMap {
		// Fetch placement to get the latest status and placement decision
		placement := &clusterv1beta1.Placement{}
		err = r.Get(ctx, types.NamespacedName{Name: placementName, Namespace: P2CodeSchedulerNamespace}, placement)
		if err != nil {
			log.Error(err, "Failed to fetch Placement")
			return err
		}

		if len(placement.Status.Conditions) < 1 {
			err := fmt.Errorf("placement decision not ready yet for %s placement", placement.Name)
			log.Error(err, "Placement decision not ready")
			return err
		}

		// Inspect the placement decision and update the status of the P2CodeSchedulingManifest accordingly
		condition, err := r.validatePlacementSatisfied(*placement, bundle)
		if err != nil {
			log.Error(err, "Unable to read placement decision for placement")
			return err
		}

		// Refetch P2CodeSchedulingManifest instance before updating the status
		if err := r.Get(ctx, types.NamespacedName{Name: p2CodeSchedulingManifest.Name, Namespace: P2CodeSchedulerNamespace}, p2CodeSchedulingManifest); err != nil {
			log.Error(err, "Failed to re-fetch P2CodeSchedulingManifest")
			return err
		}

		meta.SetStatusCondition(&p2CodeSchedulingManifest.Status.Conditions, *condition)
		p2CodeSchedulingManifest.Status.Decisions = []schedulingv1alpha1.SchedulingDecision{}
		if err := r.Status().Update(ctx, p2CodeSchedulingManifest); err != nil {
			log.Error(err, "Failed to update P2CodeSchedulingManifest status")
			return err
		}
	}

	return nil
}

func (r *P2CodeSchedulingManifestReconciler) getOwnedManifestWorkList(ownerLabel string) (workv1.ManifestWorkList, error) {
	manifestWorkList := &workv1.ManifestWorkList{}
	labelSelector, err := labels.Parse(fmt.Sprintf("%s=%s", ownershipLabel, ownerLabel))

	if err != nil {
		return workv1.ManifestWorkList{}, err
	}

	listOptions := client.ListOptions{
		LabelSelector: labelSelector,
	}

	if err := r.List(context.TODO(), manifestWorkList, &listOptions); err != nil {
		return workv1.ManifestWorkList{}, err
	}

	return *manifestWorkList, nil
}

func (r *P2CodeSchedulingManifestReconciler) deleteOwnedManifestWorkList(ownerLabel string) error {
	manifestWorkList, err := r.getOwnedManifestWorkList(ownerLabel)
	if err != nil {
		return err
	}

	for _, manifest := range manifestWorkList.Items {
		if err := r.Delete(context.TODO(), &manifest); err != nil {
			return err
		}
	}

	return nil
}

func (r *P2CodeSchedulingManifestReconciler) deleteBundles(ownerReference string) {
	delete(r.Bundles, ownerReference)
}

func (r *P2CodeSchedulingManifestReconciler) getBundle(bundleName string, ownerReference string) (*Bundle, error) {
	for _, bundle := range r.Bundles[ownerReference] {
		if bundle.name == bundleName {
			return bundle, nil
		}
	}
	return nil, fmt.Errorf("cannot find a bundle with the name %s", bundleName)
}

// Create placement if it doesnt exist
func (r *P2CodeSchedulingManifestReconciler) createPlacement(placementName string, clusterSet string, clusterPredicates []metav1.LabelSelectorRequirement, controllerReference metav1.Object) error {
	placement := &clusterv1beta1.Placement{}
	err := r.Get(context.TODO(), types.NamespacedName{Name: placementName, Namespace: P2CodeSchedulerNamespace}, placement)

	if err != nil && apierrors.IsNotFound(err) {
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

		if err = ctrl.SetControllerReference(controllerReference, placement, r.Scheme); err != nil {
			return fmt.Errorf("failed to set controller reference for placement: %w", err)
		}

		if err = r.Create(context.TODO(), placement); err != nil {
			return fmt.Errorf("failed to create Placement: %w", err)
		}

	} else if err != nil {
		return fmt.Errorf("failed to fetch Placement: %w", err)
	}

	return nil
}

func (r *P2CodeSchedulingManifestReconciler) validatePlacementSatisfied(placement clusterv1beta1.Placement, bundle *Bundle) (*metav1.Condition, error) {
	placementSatisfiedCondition := meta.FindStatusCondition(placement.Status.Conditions, "PlacementSatisfied")
	if placementSatisfiedCondition.Status == metav1.ConditionFalse {
		return r.extractFailedCondition(*placementSatisfiedCondition, placement.Spec.ClusterSets[0])
	}

	clusterName, err := r.getSelectedCluster(placement)
	if err != nil {
		return &metav1.Condition{}, fmt.Errorf("unable to read placement decision for %s placement", placement.Name)
	}
	bundle.clusterName = clusterName
	message := fmt.Sprintf("Bundle based on %s workload to be deployed on %s cluster", bundle.name, clusterName)
	return &metav1.Condition{Type: schedulingInProgress, Status: metav1.ConditionTrue, Reason: "PlacementDecisionReady", Message: message}, nil
}

func (r *P2CodeSchedulingManifestReconciler) extractFailedCondition(placementCondition metav1.Condition, managedClusterSetName string) (*metav1.Condition, error) {
	condition := metav1.Condition{Type: schedulingFailed, Status: metav1.ConditionTrue, Reason: placementCondition.Reason, Message: placementCondition.Message}

	if placementCondition.Reason == "NoManagedClusterMatched" {
		condition.Reason = "SchedulingAnnotationsUnsatisfied"
		condition.Message = fmt.Sprintf("Unable to find a suitable location for the workload as there are no managed clusters in the %s managed cluster set that satisfy the annotations requested", managedClusterSetName)
	}

	if placementCondition.Reason == "NoIntersection" {
		exists, err := r.doesManagedClusterSetExist(managedClusterSetName)

		if err != nil {
			return nil, err
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
			return workv1.ManifestWork{}, &NoNamespaceError{errorMessage}
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
		return &ManifestWorkNotReady{errorMessage}
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

func (r *P2CodeSchedulingManifestReconciler) getSelectedCluster(placement clusterv1beta1.Placement) (string, error) {
	placementDecisionName := placement.Status.DecisionGroups[0].Decisions[0]
	placementDecision := &clusterv1beta1.PlacementDecision{}
	err := r.Get(context.TODO(), types.NamespacedName{Name: placementDecisionName, Namespace: P2CodeSchedulerNamespace}, placementDecision)
	if err != nil {
		return "", err
	}

	return placementDecision.Status.Decisions[0].ClusterName, nil
}

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

func validateAnnotationsSupported(p2codeSchedulingManifest *schedulingv1alpha1.P2CodeSchedulingManifest) (bool, error) {
	for _, annotation := range p2codeSchedulingManifest.Spec.GlobalAnnotations {
		if !utils.IsAnnotationSupported(annotation) {
			return false, fmt.Errorf("%s is an unsupported annotation", annotation)
		}
	}

	for _, workloadAnnotation := range p2codeSchedulingManifest.Spec.WorkloadAnnotations {
		for _, annotation := range workloadAnnotation.Annotations {
			if !utils.IsAnnotationSupported(annotation) {
				return false, fmt.Errorf("%s is an unsupported annotation", annotation)
			}
		}
	}

	return true, nil
}

func assignWorkloadAnnotations(workloads ResourceSet, workloadAnnotations []schedulingv1alpha1.WorkloadAnnotation) error {
	for index, workloadAnnotation := range workloadAnnotations {
		// Ensure that all the workload annotations are filter annotations
		// Use the extractPlacementRules function to get a list of the filter annotations
		if len(utils.ExtractPlacementRules(workloadAnnotation.Annotations)) != len(workloadAnnotation.Annotations) {
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

func extractPodSpec(workload Resource) (*corev1.PodSpec, error) {
	switch kind := workload.metadata.groupVersionKind.Kind; kind {
	case "Deployment":
		deployment := &appsv1.Deployment{}
		if err := json.Unmarshal(workload.manifest.Raw, deployment); err != nil {
			return nil, err
		}
		return &deployment.Spec.Template.Spec, nil
	case "StatefulSet":
		statefulset := &appsv1.StatefulSet{}
		if err := json.Unmarshal(workload.manifest.Raw, statefulset); err != nil {
			return nil, err
		}
		return &statefulset.Spec.Template.Spec, nil
	case "DaemonSet":
		daemonset := &appsv1.DaemonSet{}
		if err := json.Unmarshal(workload.manifest.Raw, daemonset); err != nil {
			return nil, err
		}
		return &daemonset.Spec.Template.Spec, nil
	case "Job":
		job := &batchv1.Job{}
		if err := json.Unmarshal(workload.manifest.Raw, job); err != nil {
			return nil, err
		}
		return &job.Spec.Template.Spec, nil
	case "CronJob":
		cronJob := &batchv1.CronJob{}
		if err := json.Unmarshal(workload.manifest.Raw, cronJob); err != nil {
			return nil, err
		}
		return &cronJob.Spec.JobTemplate.Spec.Template.Spec, nil
	default:
		return nil, fmt.Errorf("unable to extract the pod spec for workload %s of type %s", workload.metadata.name, workload.metadata.groupVersionKind.Kind)
	}
}

func (e NoNamespaceError) Error() string {
	return e.message
}

func (e ManifestWorkFailedError) Error() string {
	return e.message
}

func (e ManifestWorkNotReady) Error() string {
	return e.message
}

func (r1 Resource) Equals(r2 Resource) bool {
	return r1.metadata.name == r2.metadata.name && r1.metadata.namespace == r2.metadata.namespace && r1.metadata.groupVersionKind.Kind == r2.metadata.groupVersionKind.Kind
}

func (resourceSet *ResourceSet) Add(r *Resource) {
	for _, resource := range *resourceSet {
		if resource.Equals(*r) {
			return
		}
	}

	*resourceSet = append(*resourceSet, r)
}

func (resourceSet *ResourceSet) Find(name string, kind string) (*Resource, error) {
	for _, resource := range resourceSet.FilterByKind(kind) {
		if resource.metadata.name == name {
			return resource, nil
		}
	}

	return nil, fmt.Errorf("cannot find a resource of type %s with the name %s", kind, name)
}

func (resourceSet *ResourceSet) FindWorkload(name string) (*Resource, error) {
	for _, resource := range *resourceSet {
		if resource.metadata.name == name && (resource.metadata.groupVersionKind.Group == "apps" || resource.metadata.groupVersionKind.Group == "batch") {
			return resource, nil
		}
	}

	return nil, fmt.Errorf("cannot find a workload resource with the name %s", name)
}

func (resourceSet *ResourceSet) FilterByKind(kind string) ResourceSet {
	list := ResourceSet{}
	for _, resource := range *resourceSet {
		if resource.metadata.groupVersionKind.Kind == kind {
			list = append(list, resource)
		}
	}

	return list
}

func (resourceSet *ResourceSet) Categorise() (workloads ResourceSet, ancillaryResources ResourceSet) {
	for _, resource := range *resourceSet {
		switch group := resource.metadata.groupVersionKind.Group; group {
		// Ancillary resources are in the core API group
		case "":
			ancillaryResources.Add(resource)
		// Workload resources are in the apps API group
		case "apps":
			workloads.Add(resource)
		// Jobs and CronJobs in the batch API group are considered a workload resource
		case "batch":
			workloads.Add(resource)
		}
	}

	return
}

// SetupWithManager sets up the controller with the Manager.
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
