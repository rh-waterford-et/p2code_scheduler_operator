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
	"fmt"
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
	typeMisconfigured       = "Misconfigured"
	typeUnknown             = "Unknown"
)

// P2CodeSchedulingManifestReconciler reconciles a P2CodeSchedulingManifest object
type P2CodeSchedulingManifestReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	Bundles  []*Bundle
}

// TODO metadata field that is set as the unstructured object created to replace name, labels, kind, namespace
type Resource struct {
	placed    bool
	kind      string
	name      string
	labels    map[string]string
	namespace string
	manifest  runtime.RawExtension
}

type ResourceCollection struct {
	workloads          []Resource
	ancillaryResources []Resource
}

type Bundle struct {
	mainWorkload                *Resource
	ancillaryResourcesManifests []runtime.RawExtension
	placementName               string
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
		errorMessage := "ignoring resource as it is not in the p2code-scheduler-system namespace"

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

	// if global set and workload empty {
	// createPlacement - 1 placement
	//}

	// if global empty and workload empty - everything goes to random cluster

	// if global set and workload set {
	// createPlacements
	// }

	// if global empty and workload set - bundle workloads to specified cluster and bundle remaining to random cluster

	// If no global nor workload annotations are specified examine the workload resource requests to make placement rules
	// TODO calculateWorkloadResourceRequests(resourceCollection.workloads)
	// func needs to be generic for global bundle and workload bundle case

	// Q is there case when dont make a bundle ??
	// when reconcile check if a bundle already exists and update it

	commonPlacementRules := extractPlacementRules(p2CodeSchedulingManifest.Spec.GlobalAnnotations)

	// Check if any workload annotations specified
	// Bundle workload and its ancillary resources with the annotation
	if len(p2CodeSchedulingManifest.Spec.WorkloadAnnotations) > 0 {
		for index, workloadAnnotation := range p2CodeSchedulingManifest.Spec.WorkloadAnnotations {
			workload, err := getResource(resourceCollection.workloads, workloadAnnotation.Name)
			if err != nil {
				errorMessage := fmt.Sprintf("Invalid workload name for workload annotation %d", index+1)
				log.Error(err, errorMessage)

				meta.SetStatusCondition(&p2CodeSchedulingManifest.Status.Conditions, metav1.Condition{Type: typeMisconfigured, Status: metav1.ConditionTrue, Reason: "InvalidWorkloadName", Message: errorMessage + "," + err.Error()})
				p2CodeSchedulingManifest.Status.Decisions = []schedulingv1alpha1.SchedulingDecision{}
				if err := r.Status().Update(ctx, p2CodeSchedulingManifest); err != nil {
					log.Error(err, "Failed to update P2CodeSchedulingManifest status")
					return ctrl.Result{}, err
				}

				return ctrl.Result{}, err
			}

			bundle := r.bundleWorkload(workload)

			// calculateWorkloadResourceRequests(workload)
			bundle.addAncillaryManifests(resourceCollection.ancillaryResources)

			// Create placement for bundle if it doesnt exist
			placementName := fmt.Sprintf("%s-bundle-placement", workload.name)
			placement := &clusterv1beta1.Placement{}
			err = r.Get(ctx, types.NamespacedName{Name: placementName, Namespace: P2CodeSchedulerNamespace}, placement)

			if err != nil && apierrors.IsNotFound(err) {
				var placementRules []metav1.LabelSelectorRequirement
				additionalPlacementRules := extractPlacementRules(workloadAnnotation.Annotations)
				placementRules = slices.Concat(commonPlacementRules, additionalPlacementRules)

				placement = r.generatePlacementForBundle(placementName, P2CodeSchedulerNamespace, placementRules)

				if err = ctrl.SetControllerReference(p2CodeSchedulingManifest, placement, r.Scheme); err != nil {
					log.Error(err, "Failed to set controller reference for placement")
					return ctrl.Result{}, err
				}

				log.Info("Creating placement", "Placement name", placementName)
				if err = r.Create(ctx, placement); err != nil {
					log.Error(err, "Failed to create Placement")
					return ctrl.Result{}, err
				}

				bundle.placementName = placementName

			} else if err != nil {
				log.Error(err, "Failed to fetch Placement")
				return ctrl.Result{}, err
			}
		}
	}

	// All manifests specified within the P2CodeSchedulingManifest spec must be successfully placed
	// If one manifest fails to be placed, no other manifest should run as the overall scheduling strategy failed
	// Since all manifests are contained within a bundle verify that all bundles have a placement decision before creating the corresponding ManifestWorks
	manifestWorkList := []workv1.ManifestWork{}
	for _, bundle := range r.Bundles {
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
				manifestWorkNamespace, err := r.getSelectedCluster(ctx, placement)
				if err != nil {
					log.Error(err, "Unable to read placement decision for placement")
					return ctrl.Result{}, err
				}

				manifestWork := &workv1.ManifestWork{}
				manifestWorkName := fmt.Sprintf("%s-bundle-manifestwork", bundle.mainWorkload.name)
				err = r.Get(ctx, types.NamespacedName{Name: manifestWorkName, Namespace: manifestWorkNamespace}, manifestWork)
				// Define ManifestWork to be created if a ManifestWork doesnt exist for this bundle
				if err != nil && apierrors.IsNotFound(err) {
					manifests := bundle.ancillaryResourcesManifests
					manifests = append(manifests, bundle.mainWorkload.manifest)
					newManifestWork := r.generateManifestWorkForBundle(manifestWorkName, manifestWorkNamespace, p2CodeSchedulingManifest.Name, manifests)
					manifestWorkList = append(manifestWorkList, newManifestWork)
				}

				infoMessage := fmt.Sprintf("Placement decision ready for bundle based on %s workload, bundle to be deployed on %s cluster", bundle.mainWorkload.name, manifestWorkNamespace)
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
	for _, bundle := range r.Bundles {
		placement := &clusterv1beta1.Placement{}
		err = r.Get(ctx, types.NamespacedName{Name: bundle.placementName, Namespace: P2CodeSchedulerNamespace}, placement)
		if err != nil {
			log.Error(err, "Failed to fetch Placement")
			return ctrl.Result{}, err
		}

		clusterSelected, err := r.getSelectedCluster(ctx, placement)
		if err != nil {
			log.Error(err, "Unable to read placement decision for placement")
			return ctrl.Result{}, err
		}

		decision := schedulingv1alpha1.SchedulingDecision{WorkloadName: bundle.mainWorkload.name, ClusterSelected: clusterSelected}
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
func (b *Bundle) addAncillaryManifests(ancillaryResources []Resource) error {

	// TODO test and check what happens if no namespace given
	if b.mainWorkload.namespace != "default" {
		namespaceResource, err := getResourceByKind(ancillaryResources, b.mainWorkload.namespace, "Namespace")
		if err != nil {
			return err
		}

		b.ancillaryResourcesManifests = append(b.ancillaryResourcesManifests, namespaceResource.manifest)
	}

	podSpec, err := extractPodSpec(b.mainWorkload)
	if err != nil {
		return err
	}

	// TODO suport later imagePullSecrets
	// Need to consider nodeSelector, tolerations ??

	// Later could support other types and check for aws and azure types
	// look at storage classes
	if len(podSpec.Volumes) > 0 {
		for _, volume := range podSpec.Volumes {
			if volume.PersistentVolumeClaim != nil {
				pvcResource, err := getResourceByKind(ancillaryResources, volume.PersistentVolumeClaim.ClaimName, "PersistentVolumeClaim")
				if err != nil {
					return err
				}

				b.ancillaryResourcesManifests = append(b.ancillaryResourcesManifests, pvcResource.manifest)

				// Later check for storage class
				// pvc := &corev1.PersistentVolumeClaim{}
				// if err := json.Unmarshal(pvcResource.manifest.Raw, pvc); err != nil {
				// 	return err
				// }
			}

			if volume.ConfigMap != nil {
				cmResource, err := getResourceByKind(ancillaryResources, volume.ConfigMap.Name, "ConfigMap")
				if err != nil {
					return err
				}

				b.ancillaryResourcesManifests = append(b.ancillaryResourcesManifests, cmResource.manifest)
			}

			if volume.Secret != nil {
				secretResource, err := getResourceByKind(ancillaryResources, volume.Secret.SecretName, "Secret")
				if err != nil {
					return err
				}

				b.ancillaryResourcesManifests = append(b.ancillaryResourcesManifests, secretResource.manifest)
			}
		}
	}

	if podSpec.ServiceAccountName != "" {
		saResource, err := getResourceByKind(ancillaryResources, podSpec.ServiceAccountName, "ServiceAccount")
		if err != nil {
			return err
		}

		b.ancillaryResourcesManifests = append(b.ancillaryResourcesManifests, saResource.manifest)
	}

	// Examine Containers and InitContainers for ancillary resources
	containers := podSpec.Containers
	containers = append(containers, podSpec.InitContainers...)

	// TODO examine resource requests for container
	for _, container := range containers {
		if len(container.EnvFrom) > 0 {
			for _, envSource := range container.EnvFrom {
				if envSource.ConfigMapRef != nil {
					cmResource, err := getResourceByKind(ancillaryResources, envSource.ConfigMapRef.Name, "ConfigMap")
					if err != nil {
						return err
					}

					b.ancillaryResourcesManifests = append(b.ancillaryResourcesManifests, cmResource.manifest)
				}

				if envSource.SecretRef != nil {
					secretResource, err := getResourceByKind(ancillaryResources, envSource.SecretRef.Name, "Secret")
					if err != nil {
						return err
					}

					b.ancillaryResourcesManifests = append(b.ancillaryResourcesManifests, secretResource.manifest)
				}
			}
		}

		if len(container.Env) > 0 {
			for _, envVar := range container.Env {
				if envVar.ValueFrom != nil {
					if envVar.ValueFrom.ConfigMapKeyRef != nil {
						cmResource, err := getResourceByKind(ancillaryResources, envVar.ValueFrom.ConfigMapKeyRef.Name, "ConfigMap")
						if err != nil {
							return err
						}

						b.ancillaryResourcesManifests = append(b.ancillaryResourcesManifests, cmResource.manifest)
					}

					if envVar.ValueFrom.SecretKeyRef != nil {
						secretResource, err := getResourceByKind(ancillaryResources, envVar.ValueFrom.SecretKeyRef.Name, "Secret")
						if err != nil {
							return err
						}

						b.ancillaryResourcesManifests = append(b.ancillaryResourcesManifests, secretResource.manifest)
					}
				}
			}
		}
	}

	// Look into securityContextProfile under container and pod for later version

	// TODO test this case
	// Add services to the bundle
	if b.mainWorkload.kind == "StatefulSet" {
		// If the workload is a StatefulSet the associated service can be found in the ServiceName field of its spec
		// volumeClaimTemplate is a list of pvc, not reference to pvc, look at storage classes
		statefulset := &appsv1.StatefulSet{}
		if err := json.Unmarshal(b.mainWorkload.manifest.Raw, statefulset); err != nil {
			return err
		}

		svcResource, err := getResourceByKind(ancillaryResources, statefulset.Spec.ServiceName, "Service")
		if err != nil {
			return err
		}

		b.ancillaryResourcesManifests = append(b.ancillaryResourcesManifests, svcResource.manifest)
	} else {
		// Get a list of all services and check if the service selector matches the labels on the workload
		services := listResourceByKind(ancillaryResources, "Service")
		for _, service := range services {
			svc := &corev1.Service{}
			if err := json.Unmarshal(service.manifest.Raw, svc); err != nil {
				return err
			}

			if len(svc.Spec.Selector) > 0 {
				for k, v := range svc.Spec.Selector {
					value, ok := b.mainWorkload.labels[k]

					if ok && value == v {
						b.ancillaryResourcesManifests = append(b.ancillaryResourcesManifests, service.manifest)
					}
				}
			}
		}
	}

	return nil
}

func categoriseManifests(manifests []runtime.RawExtension) (ResourceCollection, error) {
	resourceCollection := ResourceCollection{}
	for _, manifest := range manifests {
		object := &unstructured.Unstructured{}
		if err := object.UnmarshalJSON(manifest.Raw); err != nil {
			return resourceCollection, err
		}

		resource := Resource{kind: object.GetKind(), name: object.GetName(), labels: object.GetLabels(), namespace: object.GetNamespace(), manifest: manifest}
		fmt.Printf("Kind %s and Group %s", object.GetKind(), object.GetObjectKind().GroupVersionKind().Group)
		switch group := object.GetObjectKind().GroupVersionKind().Group; group {
		// Ancillary services are in the core API group
		case "":
			resourceCollection.ancillaryResources = append(resourceCollection.ancillaryResources, resource)
		// Workload resources are in the apps API group
		case "apps":
			resourceCollection.workloads = append(resourceCollection.workloads, resource)
		// Jobs and CronJobs in the batch API group are considered a workload resource
		case "batch":
			resourceCollection.workloads = append(resourceCollection.workloads, resource)
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

// Check if bundle exists for workload and if not create a bundle
func (r *P2CodeSchedulingManifestReconciler) bundleWorkload(workload *Resource) *Bundle {
	for _, bundle := range r.Bundles {
		if bundle.mainWorkload.name == workload.name {
			return bundle
		}
	}
	bundle := &Bundle{mainWorkload: workload}
	r.Bundles = append(r.Bundles, bundle)
	return bundle
}

func (r *P2CodeSchedulingManifestReconciler) generatePlacementForBundle(name string, namespace string, placementRules []metav1.LabelSelectorRequirement) *clusterv1beta1.Placement {
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

func (r *P2CodeSchedulingManifestReconciler) generateManifestWorkForBundle(name string, namespace string, ownershipLabel string, bundeledManifests []runtime.RawExtension) workv1.ManifestWork {
	manifestList := []workv1.Manifest{}

	for _, manifest := range bundeledManifests {
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
				ownershipLabel: ownershipLabel,
			},
		},
		Spec: workv1.ManifestWorkSpec{
			Workload: workv1.ManifestsTemplate{
				Manifests: manifestList,
			},
		},
	}

	return manifestWork
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

// TODO might to consider namespace
func getResource(resources []Resource, resourceName string) (*Resource, error) {
	for _, resource := range resources {
		if resource.name == resourceName {
			return &resource, nil
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

func extractPodSpec(workload *Resource) (*corev1.PodSpec, error) {
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

// SetupWithManager sets up the controller with the Manager.
func (r *P2CodeSchedulingManifestReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&schedulingv1alpha1.P2CodeSchedulingManifest{}).
		Owns(&clusterv1beta1.Placement{}).
		Owns(&workv1.ManifestWork{}).
		Complete(r)
}
