package controller

import (
	"context"
	"fmt"
	"regexp"
	"slices"

	networkoperatorv1alpha1 "github.com/rh-waterford-et/ac3_networkoperator/api/v1alpha1"
	schedulingv1alpha1 "github.com/rh-waterford-et/p2code-scheduler-operator/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
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

func (r *P2CodeSchedulingManifestReconciler) getNetworkConnections(p2CodeSchedulingManifest *schedulingv1alpha1.P2CodeSchedulingManifest) ([]NetworkConnection, error) {
	networkConnections := []NetworkConnection{}
	for _, bundle := range r.Bundles[p2CodeSchedulingManifest.Name] {
		if len(bundle.externalConnections) < 1 {
			continue
		}

		connections, err := bundle.buildNetworkConnection(r.Bundles[p2CodeSchedulingManifest.Name])
		if err != nil {
			return nil, err
		}

		networkConnections = append(networkConnections, connections...)
	}

	return networkConnections, nil
}

func (r *P2CodeSchedulingManifestReconciler) registerNetworkLinks(ctx context.Context, networkConnections []NetworkConnection) error {
	multiClusterNetwork, err := r.fetchMultiClusterNetwork(ctx)
	if err != nil {
		return err
	}

	links := multiClusterNetwork.Spec.Links
	for _, networkConnection := range networkConnections {
		if isLinkRequired(networkConnection) {
			// Check if a link exists for a given network path
			link := getLink(links, networkConnection)
			if link != nil {
				// Add connectionName to the link's list of services if necessary
				// TODO confirm if there should be a check to see if the ports match - should there be a list of port service mappings in the MultiClusterLink
				if !slices.Contains(link.Services, networkConnection.service.serviceName) {
					link.Services = append(link.Services, networkConnection.service.serviceName)
				}
			} else {
				// Create new MultiClusterLink
				link := networkoperatorv1alpha1.MultiClusterLink{
					SourceCluster:   networkConnection.source.cluster,
					SourceNamespace: networkConnection.source.namespace,
					TargetCluster:   networkConnection.target.cluster,
					TargetNamespace: networkConnection.target.namespace,
					Services:        []string{networkConnection.service.serviceName},
					Port:            networkConnection.service.port,
				}

				links = append(links, link)
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
				Links: []networkoperatorv1alpha1.MultiClusterLink{},
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

func isMultiClusterNetworkInstalled() (bool, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return false, fmt.Errorf("%w", err)
	}

	httpClient, err := rest.HTTPClientFor(cfg)
	if err != nil {
		return false, fmt.Errorf("%w", err)
	}

	restMapper, err := apiutil.NewDynamicRESTMapper(cfg, httpClient)
	if err != nil {
		return false, fmt.Errorf("%w", err)
	}

	gvk := networkoperatorv1alpha1.GroupVersion.WithKind("MultiClusterNetwork")
	_, err = restMapper.RESTMapping(
		schema.GroupKind{Group: gvk.Group, Kind: gvk.Kind},
		gvk.Version,
	)
	if err == nil {
		return true, nil
	}

	if meta.IsNoMatchError(err) {
		return false, nil
	}

	return false, fmt.Errorf("%w", err)
}

func (b *Bundle) buildNetworkConnection(bundles []*Bundle) ([]NetworkConnection, error) {
	// Ensure the bundle contains a single workload resource
	workloads, _ := b.resources.Categorise()
	if len(workloads) > 1 {
		return nil, fmt.Errorf("bundle contains more than one workload, unable to identify the details of the network link")
	}

	// Extract the target details from Bundle b
	// b is the target since the service it depends on is elsewhere
	// Below the source of this dependent service is identified
	targetLocation := Location{cluster: b.clusterName, namespace: workloads[0].metadata.namespace}

	networkConnections := []NetworkConnection{}
	for _, connection := range b.externalConnections {
		for _, bundle := range bundles {
			// Ignore bundle that called this function
			if b.name == bundle.name {
				continue
			}

			if service := bundle.getExternalService(connection.serviceName); service != nil {
				externalConn := NetworkConnection{service: connection, source: Location{cluster: bundle.clusterName, namespace: service.metadata.namespace}, target: targetLocation}
				networkConnections = append(networkConnections, externalConn)
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

func getLink(links []networkoperatorv1alpha1.MultiClusterLink, nc NetworkConnection) *networkoperatorv1alpha1.MultiClusterLink {
	for _, link := range links {
		if link.SourceCluster == nc.source.cluster && link.TargetCluster == nc.target.cluster && link.SourceNamespace == nc.source.namespace && link.TargetNamespace == nc.target.namespace {
			return &link
		}
	}
	return nil
}
