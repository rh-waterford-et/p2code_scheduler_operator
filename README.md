# P2Code Scheduler Operator

A Kubernetes operator that provides intelligent multi-cluster workload scheduling through declarative, annotation-based placement policies. Built on Open Cluster Management (OCM), the P2Code Scheduler enables organizations to distribute workloads across federated clusters based on location, capabilities, and resource availability.

## Overview

The P2Code Scheduler Operator extends Kubernetes with custom scheduling logic for multi-cluster environments. It allows you to define placement constraints using simple annotations and automatically handles workload bundling, dependency resolution, and cross-cluster network connectivity.

### Key Capabilities

- **Annotation-Based Filtering**: Schedule workloads using declarative filter annotations (`p2code.filter.location=europe`)
- **Intelligent Bundling**: Automatically groups related resources (deployments, services, configmaps, secrets, RBAC)
- **Multi-Cluster Networking**: Integrates with AC3 MultiClusterNetwork Operator for cross-cluster service discovery
- **Flexible Scheduling**: Global or per-workload placement policies
- **OCM Integration**: Leverages Open Cluster Management for cluster federation and workload distribution
- **Comprehensive Resource Support**: Handles Kubernetes core resources, OpenShift Routes, HPA, Prometheus ServiceMonitors, and more

### Example

```yaml
apiVersion: scheduling.p2code.eu/v1alpha1
kind: P2CodeSchedulingManifest
metadata:
  name: example-app
  namespace: p2code-scheduler-system
spec:
  globalAnnotations:
    - "p2code.target.managedClusterSet=test"
    - "p2code.filter.location=europe"
    - "p2code.filter.has-gpu=true"
  manifests:
    - apiVersion: apps/v1
      kind: Deployment
      metadata:
        name: ml-service
      spec:
        # ... deployment spec
    - apiVersion: v1
      kind: Service
      metadata:
        name: ml-service
      spec:
        # ... service spec
```

This manifest schedules the `ml-service` deployment and its service to all European clusters with GPU capabilities.

## Documentation

- **[Features](docs/features.md)**: Detailed feature descriptions and capabilities
- **[Architecture](docs/architecture.md)**: System architecture, components, and design patterns
- **[Getting Started Guide](docs/getting-started.md)**: Complete installation and usage guide

## Quick Start

### Prerequisites

**Development Requirements:**
- Go v1.22.0+
- Docker v17.03+
- kubectl v1.11.3+

**Runtime Requirements:**
- Kubernetes cluster v1.11.3+ with admin access
- [Open Cluster Management (OCM)](https://open-cluster-management.io/) installed and configured
- At least one managed cluster registered with OCM
- ManagedClusterSet configured with proper bindings

**Optional:**
- AC3 MultiClusterNetwork Operator (for multi-cluster networking features)
- Prometheus Operator (for ServiceMonitor support)

See the [Getting Started Guide](docs/getting-started.md) for detailed installation instructions.

## Core Features

### Intelligent Cluster Filtering
Filter target clusters using declarative annotations:
- **Geographic location**: `p2code.filter.location=europe`
- **Kubernetes distribution**: `p2code.filter.distribution=openshift`
- **Resource capabilities**: `p2code.filter.has-gpu=true`
- **Custom properties**: Any custom cluster attribute

### Automatic Resource Bundling
The operator analyzes your manifests and automatically includes:
- Supporting services, configmaps, and secrets
- RBAC resources (ServiceAccounts, Roles, RoleBindings)
- Network policies and routes
- Horizontal Pod Autoscalers
- Prometheus ServiceMonitors
- Required namespaces

### Multi-Cluster Networking
Integrates with AC3 MultiClusterNetwork Operator to:
- Detect cross-cluster service dependencies
- Automatically register network links
- Enable service discovery across clusters

### Flexible Scheduling Modes
- **Global scheduling**: Apply same filters to all workloads
- **Per-workload scheduling**: Different placement rules per workload
- **Explicit targeting**: Pin workloads to specific clusters

See [Features documentation](docs/features.md) for complete details.

## Installation

### To Deploy on the cluster
**Build and push your image to the location specified by `IMG`:**

```sh
make docker-build docker-push IMG=<some-registry>/p2code-scheduler:tag
```

**NOTE:** This image ought to be published in the personal registry you specified.
And it is required to have access to pull the image from the working environment.
Make sure you have the proper permission to the registry if the above commands don’t work.

**Install the CRDs into the cluster:**

```sh
make install
```

**Deploy the Manager to the cluster with the image specified by `IMG`:**

```sh
make deploy IMG=<some-registry>/p2code-scheduler:tag
```

> **NOTE**: If you encounter RBAC errors, you may need to grant yourself cluster-admin
privileges or be logged in as admin.

**Create instances of your solution**
You can apply the samples (examples) from the config/sample:

```sh
kubectl apply -k config/samples/
```

>**NOTE**: Ensure that the samples has default values to test it out.

### To Uninstall
**Delete the instances (CRs) from the cluster:**

```sh
kubectl delete -k config/samples/
```

**Delete the APIs(CRDs) from the cluster:**

```sh
make uninstall
```

**UnDeploy the controller from the cluster:**

```sh
make undeploy
```

## Project Distribution

Following are the steps to build the installer and distribute this project to users.

1. Build the installer for the image built and published in the registry:

```sh
make build-installer IMG=<some-registry>/p2code-scheduler:tag
```

NOTE: The makefile target mentioned above generates an 'install.yaml'
file in the dist directory. This file contains all the resources built
with Kustomize, which are necessary to install this project without
its dependencies.

2. Using the installer

Users can just run kubectl apply -f <URL for YAML BUNDLE> to install the project, i.e.:

```sh
kubectl apply -f https://raw.githubusercontent.com/<org>/p2code-scheduler/<tag or branch>/dist/install.yaml
```

## Usage Examples

### Schedule to European Clusters

```yaml
apiVersion: scheduling.p2code.eu/v1alpha1
kind: P2CodeSchedulingManifest
metadata:
  name: euro-deployment
  namespace: p2code-scheduler-system
spec:
  globalAnnotations:
    - "p2code.target.managedClusterSet=test"
    - "p2code.filter.location=europe"
  manifests:
    - apiVersion: apps/v1
      kind: Deployment
      # ... your deployment
```

### Per-Workload Placement

```yaml
apiVersion: scheduling.p2code.eu/v1alpha1
kind: P2CodeSchedulingManifest
metadata:
  name: multi-tier-app
  namespace: p2code-scheduler-system
spec:
  globalAnnotations:
    - "p2code.target.managedClusterSet=test"
  workloadAnnotations:
    frontend:
      - "p2code.filter.location=europe"
    backend:
      - "p2code.filter.has-gpu=true"
  manifests:
    - apiVersion: apps/v1
      kind: Deployment
      metadata:
        name: frontend
      # ... spec
    - apiVersion: apps/v1
      kind: Deployment
      metadata:
        name: backend
      # ... spec
```

More examples in [config/samples/](config/samples/) and the [Getting Started Guide](docs/getting-started.md).

## Contributing

Contributions are welcome! To contribute:

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

### Development

```bash
# Run tests
make test

# Run linter
make lint

# Build operator binary
make build

# Run locally (requires proper kubeconfig)
make run
```

**NOTE:** Run `make help` for more information on all potential `make` targets

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)

## Architecture

The P2Code Scheduler Operator is built using the Kubebuilder framework and integrates deeply with Open Cluster Management. It consists of:

- **P2CodeSchedulingManifest Controller**: Main reconciliation loop
- **Resource Analyzer**: Deep introspection of Kubernetes manifests
- **Bundle Manager**: Groups related resources for deployment
- **Network Connectivity Manager**: Integrates with AC3 MultiClusterNetwork Operator
- **OCM Integration**: Leverages Placement and ManifestWork APIs

See the [Architecture documentation](docs/architecture.md) for detailed component descriptions and data flow diagrams.

## Support

- **Documentation**: See [docs/](docs/) folder
- **Examples**: Check [config/samples/](config/samples/)
- **Issues**: Report bugs and request features via GitHub Issues

## License

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

