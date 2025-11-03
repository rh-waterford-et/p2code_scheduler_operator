package controller

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	// K8s resources
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"

	// OpenShift resources
	routev1 "github.com/openshift/api/route/v1"
)

// TODO might as well analyse the resource requests in here when already extracted add a field for resource requests cpu etc to the bundle
// nolint:cyclop // not to concerned about cognitive complexity
func analyseWorkload(workload *Resource, ancillaryResources ResourceSet) (ResourceSet, AbsentResourceSet, []ServicePortPair, error) {
	resources := ResourceSet{}
	resourcesNotFound := AbsentResourceSet{}

	if workload.metadata.namespace != "default" {
		namespaceResource, err := ancillaryResources.Find(workload.metadata.namespace, "Namespace")
		if err != nil {
			return ResourceSet{}, AbsentResourceSet{}, []ServicePortPair{}, fmt.Errorf("%w", err)
		}

		resources.Add(namespaceResource)
	}

	rs, absentResources, externalConnections, err := analysePodSpec(workload, ancillaryResources)
	if err != nil {
		return ResourceSet{}, AbsentResourceSet{}, []ServicePortPair{}, fmt.Errorf("%w", err)
	}

	resources.Merge(&rs)
	resourcesNotFound.Merge(&absentResources)

	serviceResources, absentResources, err := extractNetworkResources(workload, ancillaryResources)
	if err != nil {
		return ResourceSet{}, AbsentResourceSet{}, []ServicePortPair{}, fmt.Errorf("%w", err)
	}

	resources.Merge(&serviceResources)
	resourcesNotFound.Merge(&absentResources)

	return resources, resourcesNotFound, externalConnections, nil
}

// nolint:cyclop // not to concerned about cognitive complexity
func analysePodSpec(workload *Resource, ancillaryResources ResourceSet) (ResourceSet, AbsentResourceSet, []ServicePortPair, error) {
	resources := ResourceSet{}
	resourcesNotFound := AbsentResourceSet{}

	podTemplateSpec, err := extractPodTemplateSpec(*workload)
	if err != nil {
		return ResourceSet{}, AbsentResourceSet{}, []ServicePortPair{}, fmt.Errorf("%w", err)
	}
	podSpec := podTemplateSpec.Spec

	// Open question is there a need to consider nodeSelector, tolerations

	// TODO analyse securityContextProfile under container and pod for later version

	for _, pullSecret := range podSpec.ImagePullSecrets {
		secretResource, err := ancillaryResources.Find(pullSecret.Name, "Secret")
		if err != nil {
			resourcesNotFound.Register(pullSecret.Name, "Secret")
		} else {
			resources.Add(secretResource)
		}
	}

	// Later could support other types and check for aws and azure types
	// Could also include storage classes

	for _, volume := range podSpec.Volumes {
		if volume.PersistentVolumeClaim != nil {
			pvcResource, err := ancillaryResources.Find(volume.PersistentVolumeClaim.ClaimName, "PersistentVolumeClaim")
			if err != nil {
				resourcesNotFound.Register(volume.PersistentVolumeClaim.ClaimName, "PersistentVolumeClaim")
			} else {
				resources.Add(pvcResource)
			}
		}

		if volume.ConfigMap != nil {
			cmResource, err := ancillaryResources.Find(volume.ConfigMap.Name, "ConfigMap")
			if err != nil {
				resourcesNotFound.Register(volume.ConfigMap.Name, "ConfigMap")
			} else {
				resources.Add(cmResource)
			}
		}

		if volume.Secret != nil {
			secretResource, err := ancillaryResources.Find(volume.Secret.SecretName, "Secret")
			if err != nil {
				resourcesNotFound.Register(volume.Secret.SecretName, "Secret")
			} else {
				resources.Add(secretResource)
			}
		}
	}

	// ServiceAccountName is the preferred field to use to reference a service account
	// The ServiceAccount field has been deprecated
	if podSpec.ServiceAccountName != "" {
		saResource, err := ancillaryResources.Find(podSpec.ServiceAccountName, "ServiceAccount")
		if err != nil {
			resourcesNotFound.Register(podSpec.ServiceAccountName, "ServiceAccount")
		} else {
			resources.Add(saResource)
			// Check if any ClusterRoleBindings or RoleBindings use this service account as a subject
			roleResources, missingResources, err := analyseServiceAccount(*saResource, ancillaryResources)
			if err != nil {
				return ResourceSet{}, AbsentResourceSet{}, []ServicePortPair{}, fmt.Errorf("%w", err)
			}

			resources.Merge(&roleResources)
			resourcesNotFound.Merge(&missingResources)
		}
	}

	// Examine Containers and InitContainers for ancillary resources
	containers := podSpec.Containers
	containers = append(containers, podSpec.InitContainers...)

	rs, missingResources, externalConnections, err := analyseContainers(containers, ancillaryResources)
	if err != nil {
		return ResourceSet{}, AbsentResourceSet{}, []ServicePortPair{}, fmt.Errorf("%w", err)
	}

	resources.Merge(&rs)
	resourcesNotFound.Merge(&missingResources)

	return resources, resourcesNotFound, externalConnections, nil
}

// nolint:cyclop // not to concerned about cognitive complexity
func analyseContainers(containers []corev1.Container, ancillaryResources ResourceSet) (ResourceSet, AbsentResourceSet, []ServicePortPair, error) {
	resources := ResourceSet{}
	externalConnections := []ServicePortPair{}
	resourcesNotFound := AbsentResourceSet{}

	// TODO examine resource requests for container
	for _, container := range containers {
		for _, envSource := range container.EnvFrom {
			if envSource.ConfigMapRef != nil {
				cmResource, err := ancillaryResources.Find(envSource.ConfigMapRef.Name, "ConfigMap")
				if err != nil {
					resourcesNotFound.Register(envSource.ConfigMapRef.Name, "ConfigMap")
				} else {
					resources.Add(cmResource)
				}
			}

			if envSource.SecretRef != nil {
				secretResource, err := ancillaryResources.Find(envSource.SecretRef.Name, "Secret")
				if err != nil {
					resourcesNotFound.Register(envSource.SecretRef.Name, "Secret")
				} else {
					resources.Add(secretResource)
				}
			}
		}

		// nolint:nestif // not to concerned about cognitive complexity (brainfreeze)
		for _, envVar := range container.Env {
			if envVar.ValueFrom != nil {
				if envVar.ValueFrom.ConfigMapKeyRef != nil {
					cmResource, err := ancillaryResources.Find(envVar.ValueFrom.ConfigMapKeyRef.Name, "ConfigMap")
					if err != nil {
						resourcesNotFound.Register(envVar.ValueFrom.ConfigMapKeyRef.Name, "ConfigMap")
					} else {
						resources.Add(cmResource)
					}
				}

				if envVar.ValueFrom.SecretKeyRef != nil {
					secretResource, err := ancillaryResources.Find(envVar.ValueFrom.SecretKeyRef.Name, "Secret")
					if err != nil {
						resourcesNotFound.Register(envVar.ValueFrom.SecretKeyRef.Name, "Secret")
					} else {
						resources.Add(secretResource)
					}
				}
			}

			if envVar.Value != "" && isServiceNamePortReference(envVar.Value) {
				// If the workload contains an environment variable that references a service append it to the externalConnections list
				// This list is later used to create MultiClusterNetwork resources to expose services across clusters if required
				serviceName := strings.Split(envVar.Value, ":")[0]
				port, err := strconv.Atoi(strings.Split(envVar.Value, ":")[1])

				if err != nil {
					return ResourceSet{}, AbsentResourceSet{}, []ServicePortPair{}, fmt.Errorf("%w", err)
				}

				externalConnections = append(externalConnections, ServicePortPair{serviceName: serviceName, port: port})
			}
		}
	}

	return resources, resourcesNotFound, externalConnections, nil
}

// nolint:cyclop // not to concerned about cognitive complexity
func extractNetworkResources(workload *Resource, ancillaryResources ResourceSet) (ResourceSet, AbsentResourceSet, error) {
	resources := ResourceSet{}
	resouresNotFound := AbsentResourceSet{}

	// nolint:nestif
	if workload.metadata.groupVersionKind.Kind == "StatefulSet" {
		// If the workload is a StatefulSet the associated service can be found in the ServiceName field of its spec
		// volumeClaimTemplate is a list of pvc, not reference to pvc, look at storage classes
		statefulset := &appsv1.StatefulSet{}
		if err := json.Unmarshal(workload.manifest.Raw, statefulset); err != nil {
			return ResourceSet{}, AbsentResourceSet{}, fmt.Errorf("%w", err)
		}

		svcResource, err := ancillaryResources.Find(statefulset.Spec.ServiceName, "Service")
		if err != nil {
			resouresNotFound.Register(statefulset.Spec.ServiceName, "Service")
		} else {
			resources.Add(svcResource)
		}
	}

	// Analyse the metadata of the pod template spec and extract all the labels applied to the pod
	// Get a list of all services and check if the service selector matches the metadata labels
	podTemplateSpec, err := extractPodTemplateSpec(*workload)
	if err != nil {
		return ResourceSet{}, AbsentResourceSet{}, fmt.Errorf("%w", err)
	}

	services := ancillaryResources.FilterByKind("Service")
	for _, service := range services {
		svc := &corev1.Service{}
		if err := json.Unmarshal(service.manifest.Raw, svc); err != nil {
			return ResourceSet{}, AbsentResourceSet{}, fmt.Errorf("%w", err)
		}

		for k, v := range svc.Spec.Selector {
			value, ok := podTemplateSpec.Labels[k]

			if ok && value == v {
				resources.Add(service)
				r, err := findExternalNetworkAccessResources(*service, ancillaryResources)
				if err != nil {
					return ResourceSet{}, AbsentResourceSet{}, fmt.Errorf("%w", err)
				}

				resources.Merge(&r)
			}
		}
	}

	return resources, resouresNotFound, nil
}

// nolint:cyclop // not to concerned about cognitive complexity
func analyseServiceAccount(serviceAccount Resource, ancillaryResources ResourceSet) (ResourceSet, AbsentResourceSet, error) {
	resources := ResourceSet{}
	resourcesNotFound := AbsentResourceSet{}
	clusterRoleBindings := ancillaryResources.FilterByKind("ClusterRoleBinding")
	roleBindings := ancillaryResources.FilterByKind("RoleBinding")

	for _, clusterRoleBinding := range clusterRoleBindings {
		binding := &rbacv1.ClusterRoleBinding{}
		if err := json.Unmarshal(clusterRoleBinding.manifest.Raw, binding); err != nil {
			return ResourceSet{}, AbsentResourceSet{}, fmt.Errorf("%w", err)
		}

		for _, subject := range binding.Subjects {
			if subject.Kind == "ServiceAccount" && subject.Name == serviceAccount.metadata.name && subject.Namespace == serviceAccount.metadata.namespace {
				clusterRole, err := ancillaryResources.Find(binding.RoleRef.Name, binding.RoleRef.Kind)
				if err != nil {
					resourcesNotFound.Register(binding.RoleRef.Name, binding.RoleRef.Kind)
				} else {
					resources.Add(clusterRole)
					resources.Add(clusterRoleBinding)
				}
			}
		}
	}

	for _, roleBinding := range roleBindings {
		binding := &rbacv1.RoleBinding{}
		if err := json.Unmarshal(roleBinding.manifest.Raw, binding); err != nil {
			return ResourceSet{}, AbsentResourceSet{}, fmt.Errorf("%w", err)
		}

		for _, subject := range binding.Subjects {
			if subject.Kind == "ServiceAccount" && subject.Name == serviceAccount.metadata.name && subject.Namespace == serviceAccount.metadata.namespace {
				role, err := ancillaryResources.Find(binding.RoleRef.Name, binding.RoleRef.Kind)
				if err != nil {
					resourcesNotFound.Register(binding.RoleRef.Name, binding.RoleRef.Kind)
				} else {
					resources.Add(role)
					resources.Add(roleBinding)
				}
			}
		}
	}

	return resources, resourcesNotFound, nil
}

// Group a service with any Ingress or Route resources that reference it
// nolint:cyclop // not to concerned about cognitive complexity
func findExternalNetworkAccessResources(service Resource, ancillaryResources ResourceSet) (ResourceSet, error) {
	networkResources := ResourceSet{}
	ingresses := ancillaryResources.FilterByKind("Ingress")
	routes := ancillaryResources.FilterByKind("Route")

	for _, ingress := range ingresses {
		ing := &networkingv1.Ingress{}
		if err := json.Unmarshal(ingress.manifest.Raw, ing); err != nil {
			return ResourceSet{}, fmt.Errorf("%w", err)
		}

		for _, rule := range ing.Spec.Rules {
			for _, path := range rule.IngressRuleValue.HTTP.Paths {
				if path.Backend.Service != nil && path.Backend.Service.Name == service.metadata.name {
					networkResources.Add(ingress)
				}
			}
		}
	}

	for _, route := range routes {
		rt := &routev1.Route{}
		if err := json.Unmarshal(route.manifest.Raw, rt); err != nil {
			return ResourceSet{}, fmt.Errorf("%w", err)
		}

		if rt.Spec.To.Kind == "Service" && rt.Spec.To.Name == service.metadata.name {
			networkResources.Add(route)
		}
	}

	return networkResources, nil
}

// nolint:cyclop // not to concerned about cognitive complexity
func extractPodTemplateSpec(workload Resource) (*corev1.PodTemplateSpec, error) {
	switch kind := workload.metadata.groupVersionKind.Kind; kind {
	case "Pod":
		pod := &corev1.Pod{}
		if err := json.Unmarshal(workload.manifest.Raw, pod); err != nil {
			return nil, fmt.Errorf("%w", err)
		}
		return &corev1.PodTemplateSpec{ObjectMeta: pod.ObjectMeta, Spec: pod.Spec}, nil
	case "Deployment":
		deployment := &appsv1.Deployment{}
		if err := json.Unmarshal(workload.manifest.Raw, deployment); err != nil {
			return nil, fmt.Errorf("%w", err)
		}
		return &deployment.Spec.Template, nil
	case "StatefulSet":
		statefulset := &appsv1.StatefulSet{}
		if err := json.Unmarshal(workload.manifest.Raw, statefulset); err != nil {
			return nil, fmt.Errorf("%w", err)
		}
		return &statefulset.Spec.Template, nil
	case "DaemonSet":
		daemonset := &appsv1.DaemonSet{}
		if err := json.Unmarshal(workload.manifest.Raw, daemonset); err != nil {
			return nil, fmt.Errorf("%w", err)
		}
		return &daemonset.Spec.Template, nil
	case "Job":
		job := &batchv1.Job{}
		if err := json.Unmarshal(workload.manifest.Raw, job); err != nil {
			return nil, fmt.Errorf("%w", err)
		}
		return &job.Spec.Template, nil
	case "CronJob":
		cronJob := &batchv1.CronJob{}
		if err := json.Unmarshal(workload.manifest.Raw, cronJob); err != nil {
			return nil, fmt.Errorf("%w", err)
		}
		return &cronJob.Spec.JobTemplate.Spec.Template, nil
	default:
		return nil, fmt.Errorf("unable to extract the pod spec for workload %s of type %s", workload.metadata.name, workload.metadata.groupVersionKind.Kind)
	}
}
