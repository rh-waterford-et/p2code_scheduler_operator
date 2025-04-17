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
	"maps"
	"regexp"
	"slices"
	"strings"
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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	schedulingv1alpha1 "github.com/PoolPooer/p2code-scheduler/api/v1alpha1"
	networkoperatorv1alpha1 "github.com/rh-waterford-et/ac3_networkoperator/api/v1alpha1"
	clusterv1beta1 "open-cluster-management.io/api/cluster/v1beta1"
	workv1 "open-cluster-management.io/api/work/v1"
)

const finalizer = "scheduling.p2code.eu/finalizer"
const ownershipLabel = "scheduling.p2code.eu/owner"
const P2CodeSchedulerNamespace = "p2code-scheduler-system"
const AC3NetworkNamespace = "ac3no-system"

// Status conditions corresponding to the state of the P2CodeSchedulingManifest
const (
	typeSchedulingSucceeded = "SchedulingSucceeded"
	typeSchedulingFailed    = "SchedulingFailed"
	typeFinalizing          = "Finalizing"
	typeMisconfigured       = "Misconfigured"
	typeUnknown             = "Unknown"
)

// P2CodeSchedulingManifestReconciler reconciles a P2CodeSchedulingManifest object
type P2CodeSchedulingManifestReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	Bundles  map[string][]*Bundle
}

// TODO metadata field that is set as the unstructured object created to replace name, labels, kind, namespace
type Resource struct {
	placed                      bool
	kind                        string
	name                        string
	labels                      map[string]string
	p2codeSchedulingAnnotations []string
	namespace                   string
	manifest                    runtime.RawExtension
}

type ResourceCollection struct {
	workloads          []*Resource
	ancillaryResources []Resource
}

type Bundle struct {
	name                string
	manifests           []runtime.RawExtension
	placementName       string
	externalConnections []string
	clusterName         string
}

type PlacementOptions struct {
	name                string
	controllerReference metav1.Object
	clusterPredicates   []metav1.LabelSelectorRequirement
}

type NoNamespaceError struct {
	message string
}

// +kubebuilder:rbac:groups=scheduling.p2code.eu,resources=p2codeschedulingmanifests,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=scheduling.p2code.eu,resources=p2codeschedulingmanifests/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=scheduling.p2code.eu,resources=p2codeschedulingmanifests/finalizers,verbs=update
// +kubebuilder:rbac:groups=cluster.open-cluster-management.io,resources=placements,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cluster.open-cluster-management.io,resources=placementdecisions,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups=work.open-cluster-management.io,resources=manifestworks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ac3.redhat.com,resources=ac3networks,verbs=get;list;watch;create;update;patch;delete

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

	ac3Network := &networkoperatorv1alpha1.AC3Network{}
	err := r.Get(ctx, types.NamespacedName{Name: "sample-network", Namespace: AC3NetworkNamespace}, ac3Network)
	if err != nil && apierrors.IsNotFound(err) {
		log.Info("Creating AC3Network resource")
		ac3Network = &networkoperatorv1alpha1.AC3Network{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "sample-network",
				Namespace: AC3NetworkNamespace,
			},
			Spec: networkoperatorv1alpha1.AC3NetworkSpec{
				Links: []networkoperatorv1alpha1.AC3Link{
					{
						SourceCluster:   "cluster1",
						TargetCluster:   "cluster2",
						SourceNamespace: "namespace1",
						TargetNamespace: "namespace2",
						Services:        []string{"service1"},
					},
				},
			},
		}

		if err := r.Create(ctx, ac3Network); err != nil {
			log.Error(err, "Failed to create AC3Network")
			return ctrl.Result{}, err
		}
	} else if err != nil {
		log.Error(err, "Failed to retrieve AC3Network")
	}

	if r.Bundles == nil {
		r.Bundles = make(map[string][]*Bundle)
	}

	// Get P2CodeSchedulingManifest instance
	p2CodeSchedulingManifest := &schedulingv1alpha1.P2CodeSchedulingManifest{}
	err = r.Get(ctx, req.NamespacedName, p2CodeSchedulingManifest)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Stop reconciliation if the resource is not found or has been deleted
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get P2CodeSchedulingManifest")
		return ctrl.Result{}, err
	}

	// If the status is empty set the status as unknown
	if len(p2CodeSchedulingManifest.Status.Conditions) == 0 {
		meta.SetStatusCondition(&p2CodeSchedulingManifest.Status.Conditions, metav1.Condition{Type: typeUnknown, Status: metav1.ConditionTrue, Reason: "Reconciling", Message: "Reconciliation in process"})
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

		meta.SetStatusCondition(&p2CodeSchedulingManifest.Status.Conditions, metav1.Condition{Type: typeMisconfigured, Status: metav1.ConditionTrue, Reason: strcase.ToCamel(errorTitle), Message: errorMessage})
		p2CodeSchedulingManifest.Status.Decisions = []schedulingv1alpha1.SchedulingDecision{}
		if err := r.Status().Update(ctx, p2CodeSchedulingManifest); err != nil {
			log.Error(err, "Failed to update P2CodeSchedulingManifest status")
			return ctrl.Result{}, err
		}

		return ctrl.Result{}, err
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

			r.deleteBundles(p2CodeSchedulingManifest.Name)

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

	// If no annotations at all are specified all manifests are scheduled to a random cluster
	// Create an empty placement and allow the Placement API to decide on a random cluster
	// Later take into account the resource requests of each workload
	if len(p2CodeSchedulingManifest.Spec.GlobalAnnotations) == 0 && len(p2CodeSchedulingManifest.Spec.WorkloadAnnotations) == 0 {
		// Check if a default bundle already exists
		_, err := r.getBundle("default", p2CodeSchedulingManifest.Name)
		if err != nil {
			log.Info("Creating default placement for all manifests")
			bundle := &Bundle{name: "default", manifests: p2CodeSchedulingManifest.Spec.Manifests}
			r.Bundles[p2CodeSchedulingManifest.Name] = append(r.Bundles[p2CodeSchedulingManifest.Name], bundle)
			placementName := "default-placement"
			if err := r.createPlacement(ctx, PlacementOptions{name: placementName, controllerReference: p2CodeSchedulingManifest}); err != nil {
				log.Error(err, "Failed to create placement")
				return ctrl.Result{}, err
			}

			bundle.placementName = placementName
		}
	}

	// Q is there case when dont make a bundle ??
	// when reconcile check if a bundle already exists and update it

	commonPlacementRules := extractPlacementRules(p2CodeSchedulingManifest.Spec.GlobalAnnotations)

	// If no workload annotations are specified but global annotations are provided all manifests are scheduled to a cluster matching the global annotations
	if len(p2CodeSchedulingManifest.Spec.GlobalAnnotations) > 0 && len(p2CodeSchedulingManifest.Spec.WorkloadAnnotations) == 0 {
		// Check if a global bundle already exists
		_, err := r.getBundle("global", p2CodeSchedulingManifest.Name)
		if err != nil {
			log.Info("Creating global placement for all manifests")
			bundle := &Bundle{name: "global", manifests: p2CodeSchedulingManifest.Spec.Manifests}
			r.Bundles[p2CodeSchedulingManifest.Name] = append(r.Bundles[p2CodeSchedulingManifest.Name], bundle)
			placementName := "global-placement"
			if err := r.createPlacement(ctx, PlacementOptions{name: placementName, controllerReference: p2CodeSchedulingManifest, clusterPredicates: commonPlacementRules}); err != nil {
				log.Error(err, "Failed to create placement")
				return ctrl.Result{}, err
			}

			bundle.placementName = placementName
		}
	}

	// Check if any workload annotations specified
	// Bundle workload and its ancillary resources with the annotation
	if len(p2CodeSchedulingManifest.Spec.WorkloadAnnotations) > 0 {
		// Analyse manifests to be scheduled
		resourceCollection, err := categoriseManifests(p2CodeSchedulingManifest.Spec.Manifests)
		if err != nil {
			log.Error(err, "Failed to process manifests to be scheduled")
			return ctrl.Result{}, err
		}

		// Ensure workloadAnnotations refer to a valid workload
		if err := assignWorkloadAnnotations(resourceCollection.workloads, p2CodeSchedulingManifest.Spec.WorkloadAnnotations); err != nil {
			errorMessage := "Failed to assign all workload annotations to valid workloads"
			log.Error(err, errorMessage)
			meta.SetStatusCondition(&p2CodeSchedulingManifest.Status.Conditions, metav1.Condition{Type: typeMisconfigured, Status: metav1.ConditionTrue, Reason: "InvalidWorkloadName", Message: errorMessage + ", " + err.Error()})
			p2CodeSchedulingManifest.Status.Decisions = []schedulingv1alpha1.SchedulingDecision{}
			if err := r.Status().Update(ctx, p2CodeSchedulingManifest); err != nil {
				log.Error(err, "Failed to update P2CodeSchedulingManifest status")
				return ctrl.Result{}, err
			}

			return ctrl.Result{}, err
		}

		for _, workload := range resourceCollection.workloads {
			// Check if a bundle already exists for the workload
			// Create a bundle for the workload if needed and find its ancillary resources
			bundle, err := r.getBundle(workload.name, p2CodeSchedulingManifest.Name)
			if err != nil {
				message := fmt.Sprintf("Analysing workload %s for its ancillary resources", workload.name)
				log.Info(message)
				ancillaryManifests, externalConnections, err := analyseWorkload(*workload, resourceCollection.ancillaryResources)
				var noNamespaceErr *NoNamespaceError
				if errors.As(err, &noNamespaceErr) {
					errorMessage := "Namespace omitted"
					log.Error(err, errorMessage)

					meta.SetStatusCondition(&p2CodeSchedulingManifest.Status.Conditions, metav1.Condition{Type: typeMisconfigured, Status: metav1.ConditionTrue, Reason: "MisconfiguredManifest", Message: errorMessage + ", " + err.Error()})
					p2CodeSchedulingManifest.Status.Decisions = []schedulingv1alpha1.SchedulingDecision{}
					if err := r.Status().Update(ctx, p2CodeSchedulingManifest); err != nil {
						log.Error(err, "Failed to update P2CodeSchedulingManifest status")
						return ctrl.Result{}, err
					}

					return ctrl.Result{}, err

				} else if err != nil {
					log.Error(err, "Error occurred while analysing workload for ancillary resources")
					return ctrl.Result{}, err
				}

				bundleManifests := []runtime.RawExtension{}
				bundleManifests = append(bundleManifests, workload.manifest)
				bundleManifests = append(bundleManifests, ancillaryManifests...)
				bundle = &Bundle{name: workload.name, manifests: bundleManifests, externalConnections: externalConnections}
				r.Bundles[p2CodeSchedulingManifest.Name] = append(r.Bundles[p2CodeSchedulingManifest.Name], bundle)
			}

			placementName := fmt.Sprintf("%s-bundle-placement", workload.name)
			additionalPlacementRules := extractPlacementRules(workload.p2codeSchedulingAnnotations)
			placementRules := slices.Concat(commonPlacementRules, additionalPlacementRules)
			// TODO calculateWorkloadResourceRequests(workload)

			if err := r.createPlacement(ctx, PlacementOptions{name: placementName, controllerReference: p2CodeSchedulingManifest, clusterPredicates: placementRules}); err != nil {
				log.Error(err, "Failed to create placement")
				return ctrl.Result{}, err
			}

			bundle.placementName = placementName
		}
	}

	// All manifests specified within the P2CodeSchedulingManifest spec must be successfully placed
	// If one manifest fails to be placed, no other manifest should run as the overall scheduling strategy failed
	// Since all manifests should be contained within a bundle verify that all bundles have a placement decision before creating the corresponding ManifestWorks
	log.Info("Verifying all bundles have placement decision")
	manifestWorkList := []workv1.ManifestWork{}
	placedManifests := 0
	for _, bundle := range r.Bundles[p2CodeSchedulingManifest.Name] {
		placedManifests += len(bundle.manifests)

		// Fetch placement to get the latest status and placement decision
		placement := &clusterv1beta1.Placement{}
		err = r.Get(ctx, types.NamespacedName{Name: bundle.placementName, Namespace: P2CodeSchedulerNamespace}, placement)
		if err != nil {
			log.Error(err, "Failed to fetch Placement")
			return ctrl.Result{}, err
		}

		if len(placement.Status.Conditions) > 0 {
			infoMessage := fmt.Sprintf("Checking status of %s placement", placement.Name)
			log.Info(infoMessage)

			if isPlacementSatisfied(placement.Status.Conditions) {
				clusterName, err := r.getSelectedCluster(ctx, placement)
				if err != nil {
					log.Error(err, "Unable to read placement decision for placement")
					return ctrl.Result{}, err
				}

				bundle.clusterName = clusterName

				manifestWork := &workv1.ManifestWork{}
				manifestWorkName := fmt.Sprintf("%s-bundle-manifestwork", bundle.name)
				err = r.Get(ctx, types.NamespacedName{Name: manifestWorkName, Namespace: clusterName}, manifestWork)
				// Define ManifestWork to be created if a ManifestWork doesnt exist for this bundle
				if err != nil && apierrors.IsNotFound(err) {
					newManifestWork, err := r.generateManifestWorkForBundle(manifestWorkName, clusterName, p2CodeSchedulingManifest.Name, bundle.manifests)
					var noNamespaceErr *NoNamespaceError
					if errors.As(err, &noNamespaceErr) {
						errorMessage := "Namespace omitted"
						log.Error(err, errorMessage)

						meta.SetStatusCondition(&p2CodeSchedulingManifest.Status.Conditions, metav1.Condition{Type: typeMisconfigured, Status: metav1.ConditionTrue, Reason: "MisconfiguredManifest", Message: errorMessage + ", " + err.Error()})
						p2CodeSchedulingManifest.Status.Decisions = []schedulingv1alpha1.SchedulingDecision{}
						if err := r.Status().Update(ctx, p2CodeSchedulingManifest); err != nil {
							log.Error(err, "Failed to update P2CodeSchedulingManifest status")
							return ctrl.Result{}, err
						}

						return ctrl.Result{}, err

					} else if err != nil {
						log.Error(err, "Failed to generate ManifestWork")
						return ctrl.Result{}, err
					}

					manifestWorkList = append(manifestWorkList, newManifestWork)
				}

				infoMessage := fmt.Sprintf("Placement decision ready for bundle based on %s workload, bundle to be deployed on %s cluster", bundle.name, clusterName)
				log.Info(infoMessage)

			} else {
				errorTitle := "placement failed"
				errorMessage := "Unable to find a suitable location for the workload as there are no managed clusters that satisfy the annotations requested"
				// TODO in next version add more info on why no suitable cluster was found using placement status

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
		} else {
			// If the placement decision is not ready yet run the reconcile loop again in 10 seconds to complete reconciliation
			log.Info("Placement decision not ready yet, requeuing")
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
	}

	// Ensure there are no orphaned manifests
	// Orphaned manifests can arise if there are ancillary services that are not referenced by a workload resource and are therefore not added to a bundle
	if len(p2CodeSchedulingManifest.Spec.Manifests) != placedManifests {
		errorTitle := "orphaned manifest"
		errorMessage := "Orphaned manifests found. Ensure all ancillary resources (services, configmaps, secrets, etc) are referenced by a workload resource"

		err := fmt.Errorf("%s", errorTitle)
		log.Error(err, errorMessage)

		meta.SetStatusCondition(&p2CodeSchedulingManifest.Status.Conditions, metav1.Condition{Type: typeMisconfigured, Status: metav1.ConditionTrue, Reason: strcase.ToCamel(errorTitle), Message: errorMessage})
		p2CodeSchedulingManifest.Status.Decisions = []schedulingv1alpha1.SchedulingDecision{}
		if err := r.Status().Update(ctx, p2CodeSchedulingManifest); err != nil {
			log.Error(err, "Failed to update P2CodeSchedulingManifest status")
			return ctrl.Result{}, err
		}

		return ctrl.Result{}, err
	}

	// Create ManifestWorks
	log.Info("A suitable cluster has been identified for each workload, creating ManifestWorks to schedule the workloads")
	// TODO Do need to delete manifest works already deployed if one fails to create - use deleteOwnedManifestWorkList func
	for _, manifestWork := range manifestWorkList {
		if err = r.Create(ctx, &manifestWork); err != nil {
			log.Error(err, "Failed to create ManifestWork")
			return ctrl.Result{}, err
		}
	}

	message := "All workloads have been successfully scheduled to suitable clusters"
	log.Info(message)

	schedulingDecisions := []schedulingv1alpha1.SchedulingDecision{}
	for _, bundle := range r.Bundles[p2CodeSchedulingManifest.Name] {
		decision := schedulingv1alpha1.SchedulingDecision{WorkloadName: bundle.name, ClusterSelected: bundle.clusterName}
		schedulingDecisions = append(schedulingDecisions, decision)
	}

	meta.SetStatusCondition(&p2CodeSchedulingManifest.Status.Conditions, metav1.Condition{Type: typeSchedulingSucceeded, Status: metav1.ConditionTrue, Reason: "SchedulingSuccessful", Message: message})
	p2CodeSchedulingManifest.Status.Decisions = schedulingDecisions
	if err := r.Status().Update(ctx, p2CodeSchedulingManifest); err != nil {
		log.Error(err, "Failed to update P2CodeSchedulingManifest status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// TODO might as well analyse the resource requests in here when already extracted add a field for resource requests cpu etc to the bundle
func analyseWorkload(workload Resource, ancillaryResources []Resource) ([]runtime.RawExtension, []string, error) {
	// Manifests is a map of raw manifests for ancillary resources that should be bundled with the workload
	// The name of the raw manifest is the key for this map
	manifests := map[string]runtime.RawExtension{}
	externalConnections := []string{}

	if workload.namespace == "" {
		errorMessage := fmt.Sprintf("no namespace defined for the workload %s", workload.name)
		return []runtime.RawExtension{}, []string{}, &NoNamespaceError{errorMessage}
	}

	if workload.namespace != "default" {
		if _, ok := manifests[workload.namespace]; !ok {
			namespaceResource, err := getResourceByKind(ancillaryResources, workload.namespace, "Namespace")
			if err != nil {
				return []runtime.RawExtension{}, []string{}, err
			}

			manifests[workload.namespace] = namespaceResource.manifest
		}
	}

	podSpec, err := extractPodSpec(workload)
	if err != nil {
		return []runtime.RawExtension{}, []string{}, err
	}

	// TODO suport later imagePullSecrets
	// Need to consider nodeSelector, tolerations ??

	// Later could support other types and check for aws and azure types
	// look at storage classes
	if len(podSpec.Volumes) > 0 {
		for _, volume := range podSpec.Volumes {
			if volume.PersistentVolumeClaim != nil {
				if _, ok := manifests[volume.PersistentVolumeClaim.ClaimName]; !ok {
					pvcResource, err := getResourceByKind(ancillaryResources, volume.PersistentVolumeClaim.ClaimName, "PersistentVolumeClaim")
					if err != nil {
						return []runtime.RawExtension{}, []string{}, err
					}

					manifests[volume.PersistentVolumeClaim.ClaimName] = pvcResource.manifest
				}

				// Later check for storage class
				// pvc := &corev1.PersistentVolumeClaim{}
				// if err := json.Unmarshal(pvcResource.manifest.Raw, pvc); err != nil {
				// 	return err
				// }
			}

			if volume.ConfigMap != nil {
				if _, ok := manifests[volume.ConfigMap.Name]; !ok {
					cmResource, err := getResourceByKind(ancillaryResources, volume.ConfigMap.Name, "ConfigMap")
					if err != nil {
						return []runtime.RawExtension{}, []string{}, err
					}

					manifests[volume.ConfigMap.Name] = cmResource.manifest
				}
			}

			if volume.Secret != nil {
				if _, ok := manifests[volume.Secret.SecretName]; !ok {
					secretResource, err := getResourceByKind(ancillaryResources, volume.Secret.SecretName, "Secret")
					if err != nil {
						return []runtime.RawExtension{}, []string{}, err
					}

					manifests[volume.Secret.SecretName] = secretResource.manifest
				}
			}
		}
	}

	if podSpec.ServiceAccountName != "" {
		if _, ok := manifests[podSpec.ServiceAccountName]; !ok {
			saResource, err := getResourceByKind(ancillaryResources, podSpec.ServiceAccountName, "ServiceAccount")
			if err != nil {
				return []runtime.RawExtension{}, []string{}, err
			}

			manifests[podSpec.ServiceAccountName] = saResource.manifest
		}
	}

	// Examine Containers and InitContainers for ancillary resources
	containers := podSpec.Containers
	containers = append(containers, podSpec.InitContainers...)

	// TODO examine resource requests for container
	for _, container := range containers {
		if len(container.EnvFrom) > 0 {
			for _, envSource := range container.EnvFrom {
				if envSource.ConfigMapRef != nil {
					if _, ok := manifests[envSource.ConfigMapRef.Name]; !ok {
						cmResource, err := getResourceByKind(ancillaryResources, envSource.ConfigMapRef.Name, "ConfigMap")
						if err != nil {
							return []runtime.RawExtension{}, []string{}, err
						}

						manifests[envSource.ConfigMapRef.Name] = cmResource.manifest
					}
				}

				if envSource.SecretRef != nil {
					if _, ok := manifests[envSource.SecretRef.Name]; !ok {
						secretResource, err := getResourceByKind(ancillaryResources, envSource.SecretRef.Name, "Secret")
						if err != nil {
							return []runtime.RawExtension{}, []string{}, err
						}

						manifests[envSource.SecretRef.Name] = secretResource.manifest
					}
				}
			}
		}

		if len(container.Env) > 0 {
			for _, envVar := range container.Env {
				if envVar.ValueFrom != nil {
					if envVar.ValueFrom.ConfigMapKeyRef != nil {
						if _, ok := manifests[envVar.ValueFrom.ConfigMapKeyRef.Name]; !ok {
							cmResource, err := getResourceByKind(ancillaryResources, envVar.ValueFrom.ConfigMapKeyRef.Name, "ConfigMap")
							if err != nil {
								return []runtime.RawExtension{}, []string{}, err
							}

							manifests[envVar.ValueFrom.ConfigMapKeyRef.Name] = cmResource.manifest
						}
					}

					if envVar.ValueFrom.SecretKeyRef != nil {
						if _, ok := manifests[envVar.ValueFrom.SecretKeyRef.Name]; !ok {
							secretResource, err := getResourceByKind(ancillaryResources, envVar.ValueFrom.SecretKeyRef.Name, "Secret")
							if err != nil {
								return []runtime.RawExtension{}, []string{}, err
							}

							manifests[envVar.ValueFrom.SecretKeyRef.Name] = secretResource.manifest
						}
					}
				} else if envVar.Value != "" && isExternalConnectionReference(envVar.Value) {
					// If the workload contains an environment variable that references a service append it to the externalConnections list
					// This list is later used to create AC3Network resources to expose service across clusters if required
					serviceName := strings.Split(envVar.Value, ":")[0]
					externalConnections = append(externalConnections, serviceName)
				}
			}
		}
	}

	// Look into securityContextProfile under container and pod for later version

	// TODO test this case
	// Add services to the bundle
	if workload.kind == "StatefulSet" {
		// If the workload is a StatefulSet the associated service can be found in the ServiceName field of its spec
		// volumeClaimTemplate is a list of pvc, not reference to pvc, look at storage classes
		statefulset := &appsv1.StatefulSet{}
		if err := json.Unmarshal(workload.manifest.Raw, statefulset); err != nil {
			return []runtime.RawExtension{}, []string{}, err
		}

		if _, ok := manifests[statefulset.Spec.ServiceName]; !ok {
			svcResource, err := getResourceByKind(ancillaryResources, statefulset.Spec.ServiceName, "Service")
			if err != nil {
				return []runtime.RawExtension{}, []string{}, err
			}

			manifests[statefulset.Spec.ServiceName] = svcResource.manifest
		}
	} else {
		// Get a list of all services and check if the service selector matches the labels on the workload
		services := listResourceByKind(ancillaryResources, "Service")
		for _, service := range services {
			svc := &corev1.Service{}
			if err := json.Unmarshal(service.manifest.Raw, svc); err != nil {
				return []runtime.RawExtension{}, []string{}, err
			}

			if len(svc.Spec.Selector) > 0 {
				for k, v := range svc.Spec.Selector {
					value, ok := workload.labels[k]

					if ok && value == v {
						if _, ok := manifests[svc.Name]; !ok {
							manifests[svc.Name] = service.manifest
						}
					}
				}
			}
		}
	}

	return slices.Collect(maps.Values(manifests)), externalConnections, nil
}

func categoriseManifests(manifests []runtime.RawExtension) (ResourceCollection, error) {
	resourceCollection := ResourceCollection{}
	for _, manifest := range manifests {
		object := &unstructured.Unstructured{}
		if err := object.UnmarshalJSON(manifest.Raw); err != nil {
			return resourceCollection, err
		}

		resource := Resource{kind: object.GetKind(), name: object.GetName(), labels: object.GetLabels(), namespace: object.GetNamespace(), manifest: manifest}
		switch group := object.GetObjectKind().GroupVersionKind().Group; group {
		// Ancillary services are in the core API group
		case "":
			resourceCollection.ancillaryResources = append(resourceCollection.ancillaryResources, resource)
		// Workload resources are in the apps API group
		case "apps":
			resourceCollection.workloads = append(resourceCollection.workloads, &resource)
		// Jobs and CronJobs in the batch API group are considered a workload resource
		case "batch":
			resourceCollection.workloads = append(resourceCollection.workloads, &resource)
		}
	}
	return resourceCollection, nil
}

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

func (r *P2CodeSchedulingManifestReconciler) deleteBundles(ownerReference string) {
	delete(r.Bundles, ownerReference)
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

// Check if a given string is of the format serviceName:port where
// serviceName must only contain lowercase alphanumeric characters or hyphens adhering to Kubernetes naming conventions
// and port corresponds to any valid network port
func isExternalConnectionReference(s string) bool {
	r, _ := regexp.Compile("^[a-z0-9/-]+:[0-9]+$")
	return r.MatchString(s)
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
func (r *P2CodeSchedulingManifestReconciler) createPlacement(ctx context.Context, options PlacementOptions) error {
	placement := &clusterv1beta1.Placement{}
	err := r.Get(ctx, types.NamespacedName{Name: options.name, Namespace: P2CodeSchedulerNamespace}, placement)

	if err != nil && apierrors.IsNotFound(err) {
		var numClusters int32 = 1

		spec := clusterv1beta1.PlacementSpec{
			NumberOfClusters: &numClusters,
			ClusterSets: []string{
				"global",
			},
		}

		if options.clusterPredicates != nil {
			spec.Predicates = []clusterv1beta1.ClusterPredicate{
				{
					RequiredClusterSelector: clusterv1beta1.ClusterSelector{
						ClaimSelector: clusterv1beta1.ClusterClaimSelector{
							MatchExpressions: options.clusterPredicates,
						},
					},
				},
			}
		}

		placement := &clusterv1beta1.Placement{
			ObjectMeta: metav1.ObjectMeta{
				Name:      options.name,
				Namespace: P2CodeSchedulerNamespace,
			},
			Spec: spec,
		}

		if err = ctrl.SetControllerReference(options.controllerReference, placement, r.Scheme); err != nil {
			return fmt.Errorf("failed to set controller reference for placement")
		}

		if err = r.Create(ctx, placement); err != nil {
			return fmt.Errorf("failed to create Placement")
		}

	} else if err != nil {
		return fmt.Errorf("failed to fetch Placement")
	}

	return nil
}

func (r *P2CodeSchedulingManifestReconciler) generateManifestWorkForBundle(name string, namespace string, ownerLabel string, bundeledManifests []runtime.RawExtension) (workv1.ManifestWork, error) {
	manifestList := []workv1.Manifest{}

	for _, manifest := range bundeledManifests {
		object := &unstructured.Unstructured{}
		if err := object.UnmarshalJSON(manifest.Raw); err != nil {
			return workv1.ManifestWork{}, err
		}

		if object.GetNamespace() == "" {
			errorMessage := fmt.Sprintf("invalid manifest, no namespace specified for %s %s", object.GetName(), object.GetKind())
			return workv1.ManifestWork{}, &NoNamespaceError{errorMessage}
		}

		m := workv1.Manifest{
			RawExtension: manifest,
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

func (r *P2CodeSchedulingManifestReconciler) getSelectedCluster(ctx context.Context, placement *clusterv1beta1.Placement) (string, error) {
	placementDecisionName := placement.Status.DecisionGroups[0].Decisions[0]
	placementDecision := &clusterv1beta1.PlacementDecision{}
	err := r.Get(ctx, types.NamespacedName{Name: placementDecisionName, Namespace: P2CodeSchedulerNamespace}, placementDecision)
	if err != nil {
		return "", err
	}

	return placementDecision.Status.Decisions[0].ClusterName, nil
}

func assignWorkloadAnnotations(workloads []*Resource, workloadAnnotations []schedulingv1alpha1.WorkloadAnnotation) error {
	for index, workloadAnnotation := range workloadAnnotations {
		if workload, err := getResource(workloads, workloadAnnotation.Name); err != nil {
			return fmt.Errorf("invalid workload name for workload annotation %d", index+1)
		} else {
			workload.p2codeSchedulingAnnotations = workloadAnnotation.Annotations
		}
	}
	return nil
}

// TODO might to consider namespace
func getResource(resources []*Resource, resourceName string) (*Resource, error) {
	for _, resource := range resources {
		if resource.name == resourceName {
			return resource, nil
		}
	}
	return nil, fmt.Errorf("cannot find a resource under manifests with the name %s", resourceName)
}

func getResourceByKind(resources []Resource, resourceName string, kind string) (*Resource, error) {
	for _, resource := range resources {
		if resource.kind == kind && resource.name == resourceName {
			return &resource, nil
		}
	}
	return nil, fmt.Errorf("cannot find a resource of type %s under manifests with the name %s", kind, resourceName)
}

func listResourceByKind(resources []Resource, kind string) []Resource {
	list := []Resource{}
	for _, resource := range resources {
		if resource.kind == kind {
			list = append(list, resource)
		}
	}
	return list
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

func extractPodSpec(workload Resource) (*corev1.PodSpec, error) {
	switch kind := workload.kind; kind {
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
		return nil, fmt.Errorf("unable to extract the pod spec for workload %s of type %s", workload.name, workload.kind)
	}
}

func (e NoNamespaceError) Error() string {
	return e.message
}

// SetupWithManager sets up the controller with the Manager.
func (r *P2CodeSchedulingManifestReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&schedulingv1alpha1.P2CodeSchedulingManifest{}).
		Owns(&clusterv1beta1.Placement{}).
		Owns(&workv1.ManifestWork{}).
		// Don't trigger the reconciler if an update doesnt change the metadata.generation field of the object being reconciled
		// This means that an update event for the object's status section will no trigger the reconciler
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		Complete(r)
}
