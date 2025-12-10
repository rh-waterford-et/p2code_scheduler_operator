package controller

import (
	"context"
	"fmt"
	"regexp"

	networkoperatorv1alpha1 "github.com/rh-waterford-et/ac3_networkoperator/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const MultiClusterNetworkNamespace = "ac3no-system"
const MultiClusterNetworkResourceName = "application-connectivity-definitions"

type ServicePortPair struct {
	serviceName string
	port        int
}

type Location struct {
	cluster   string
	namespace string
}

type NetworkConnection struct {
	service ServicePortPair
	source  Location
	target  Location
}

func (r *P2CodeSchedulingManifestReconciler) getNetworkConnections(bundles BundleList) ([]NetworkConnection, error) {
	networkConnections := []NetworkConnection{}
	for _, bundle := range bundles {
		if len(bundle.externalConnections) < 1 {
			continue
		}

		connections, err := bundle.buildNetworkConnection(bundles)
		if err != nil {
			return nil, err
		}

		networkConnections = append(networkConnections, connections...)
	}

	return filterNetworkConnections(networkConnections), nil
}

func (r *P2CodeSchedulingManifestReconciler) registerNetworkLinks(ctx context.Context, networkConnections []NetworkConnection) error {
	multiClusterNetwork, err := r.fetchMultiClusterNetwork(ctx)
	if err != nil {
		return err
	}

	links := multiClusterNetwork.Spec.Links
	for _, networkConnection := range networkConnections {
		// Create a networkoperatorv1alpha1.ServicePortPair from the network connection details
		service := &networkoperatorv1alpha1.ServicePortPair{Name: networkConnection.service.serviceName, Port: networkConnection.service.port}

		// Check if a link exists for a given network path
		_, link := getLink(links, networkConnection)
		if link != nil {
			// Update the services of the link if necessary
			link.AddService(service)
		} else {
			// Create new MultiClusterLink
			link := &networkoperatorv1alpha1.MultiClusterLink{
				SourceCluster:   networkConnection.source.cluster,
				SourceNamespace: networkConnection.source.namespace,
				TargetCluster:   networkConnection.target.cluster,
				TargetNamespace: networkConnection.target.namespace,
				Services:        []*networkoperatorv1alpha1.ServicePortPair{service},
			}

			links = append(links, link)
		}
	}

	// Update the MultiClusterNetwork resource
	multiClusterNetwork.Spec.Links = links
	if err = r.Update(ctx, multiClusterNetwork); err != nil {
		return fmt.Errorf("%w", err)
	}

	return nil
}

func (r *P2CodeSchedulingManifestReconciler) deleteNetworkLinks(ctx context.Context, networkConnections []NetworkConnection) error {
	multiClusterNetwork, err := r.fetchMultiClusterNetwork(ctx)
	if err != nil {
		return err
	}

	links := multiClusterNetwork.Spec.Links
	for _, networkConnection := range networkConnections {
		linkIndex, link := getLink(links, networkConnection)
		if link != nil {
			serviceIndex, _ := link.GetService(networkConnection.service.serviceName)
			// Delete the link if it does not connect any other services
			if len(link.Services) == 1 && serviceIndex != -1 {
				links = append(links[:linkIndex], links[linkIndex+1:]...)
			} else if serviceIndex != -1 {
				// Remove the service from the links service list
				link.Services = append(link.Services[:serviceIndex], link.Services[serviceIndex+1:]...)
			}
		}
	}

	// Update the MultiClusterNetwork resource
	multiClusterNetwork.Spec.Links = links
	if err = r.Update(ctx, multiClusterNetwork); err != nil {
		return fmt.Errorf("%w", err)
	}

	return nil
}

func (r *P2CodeSchedulingManifestReconciler) fetchMultiClusterNetwork(ctx context.Context) (*networkoperatorv1alpha1.MultiClusterNetwork, error) {
	multiClusterNetwork := &networkoperatorv1alpha1.MultiClusterNetwork{}
	err := r.Get(ctx, types.NamespacedName{Name: MultiClusterNetworkResourceName, Namespace: MultiClusterNetworkNamespace}, multiClusterNetwork)
	// Create MultiClusterNetwork if it doesnt exist
	if err != nil && apierrors.IsNotFound(err) {
		multiClusterNetwork = &networkoperatorv1alpha1.MultiClusterNetwork{
			ObjectMeta: metav1.ObjectMeta{
				Name:      MultiClusterNetworkResourceName,
				Namespace: MultiClusterNetworkNamespace,
			},
			Spec: networkoperatorv1alpha1.MultiClusterNetworkSpec{
				Links: []*networkoperatorv1alpha1.MultiClusterLink{},
			},
		}

		if err := r.Create(ctx, multiClusterNetwork); err != nil {
			return nil, fmt.Errorf("%w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("%w", err)
	}

	return multiClusterNetwork, nil
}

func (b *Bundle) buildNetworkConnection(bundles BundleList) ([]NetworkConnection, error) {
	// Ensure the bundle contains a single workload resource
	workloads, _ := b.resources.Categorise()
	if len(workloads) > 1 {
		return nil, fmt.Errorf("bundle contains more than one workload, unable to identify the details of the network link")
	}

	// Extract the target details from Bundle b
	// b is the target since the service it depends on is elsewhere
	// Below the source of this dependent service is identified
	targetLocation := Location{cluster: b.schedulingDetails.clusterName, namespace: workloads[0].metadata.namespace}

	networkConnections := []NetworkConnection{}
	for _, connection := range b.externalConnections {
		for _, bundle := range bundles {
			// Ignore bundle that called this function
			if b.name == bundle.name {
				continue
			}

			if service := bundle.getExternalService(connection.serviceName); service != nil {
				nc := NetworkConnection{service: connection, source: Location{cluster: bundle.schedulingDetails.clusterName, namespace: service.metadata.namespace}, target: targetLocation}
				networkConnections = append(networkConnections, nc)
			}
		}
	}

	return networkConnections, nil
}

func (b *Bundle) getExternalService(connectionName string) *Resource {
	for _, service := range b.resources.FilterByKind("Service") {
		if service.metadata.name == connectionName {
			return service
		}
	}
	return nil
}

// Helper function to filter the network connections and return a list of connections that must be enabled through the MultiClusterNetwork feature
func filterNetworkConnections(networkConnections []NetworkConnection) []NetworkConnection {
	connections := []NetworkConnection{}
	for _, nc := range networkConnections {
		if isLinkRequired(nc) {
			connections = append(connections, nc)
		}
	}

	return connections
}

// Check if a given string is of the format serviceName:port where
// serviceName must only contain lowercase alphanumeric characters or hyphens adhering to Kubernetes naming conventions
// and port corresponds to any valid network port
func isServiceNamePortReference(s string) bool {
	r := regexp.MustCompile("^[a-z0-9/-]+:[0-9]+$")
	return r.MatchString(s)
}

func isLinkRequired(nc NetworkConnection) bool {
	return !(nc.source.cluster == nc.target.cluster && nc.source.namespace == nc.target.namespace)
}

func getLink(links []*networkoperatorv1alpha1.MultiClusterLink, nc NetworkConnection) (int, *networkoperatorv1alpha1.MultiClusterLink) {
	for index, link := range links {
		if link.SourceCluster == nc.source.cluster && link.TargetCluster == nc.target.cluster && link.SourceNamespace == nc.source.namespace && link.TargetNamespace == nc.target.namespace {
			return index, link
		}
	}
	return -1, nil
}
