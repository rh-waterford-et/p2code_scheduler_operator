# Features

The P2Code Scheduler Operator provides intelligent multi-cluster workload scheduling capabilities for Kubernetes environments federated through Open Cluster Management (OCM). This document outlines the key features and capabilities of the operator.

## Overview

The P2Code Scheduler extends Kubernetes with custom scheduling logic that enables workloads to be intelligently distributed across multiple clusters based on declarative placement constraints. It integrates seamlessly with OCM to provide a powerful, annotation-driven scheduling framework.

## Core Features

### 1. Annotation-Based Cluster Filtering

The operator uses a declarative annotation syntax to filter and select target clusters based on various criteria:

#### Supported Filter Dimensions

- **Geographic Location**: Filter clusters by physical location
  ```yaml
  p2code.filter.location=europe
  p2code.filter.location=greece
  p2code.filter.location=italy
  ```

- **Kubernetes Distribution**: Target specific Kubernetes distributions
  ```yaml
  p2code.filter.distribution=kubernetes
  p2code.filter.distribution=openshift
  ```

- **Resource Capabilities**: Select clusters with specific hardware or resources
  ```yaml
  p2code.filter.has-gpu=true
  p2code.filter.edge-device=true
  ```

- **Custom Cluster Properties**: Define and filter by any custom cluster attributes
  ```yaml
  p2code.filter.environment=production
  p2code.filter.tier=premium
  ```

#### Annotation Scopes

- **Global Annotations**: Apply filters to all manifests in a scheduling request
- **Workload Annotations**: Apply filters to specific workloads for granular control
- **Explicit Cluster Targeting**: Pin workloads to specific named clusters

### 2. Intelligent Resource Bundling

The operator automatically analyzes Kubernetes manifests and creates dependency-aware bundles that ensure complete workload deployment.

#### Bundle Components

When you specify a primary workload (e.g., a Deployment), the operator automatically includes:

- **Primary Workload Resources**
  - Deployments
  - StatefulSets
  - DaemonSets
  - Jobs and CronJobs

- **Supporting Resources**
  - Services (ClusterIP, NodePort, LoadBalancer)
  - ConfigMaps
  - Secrets
  - Persistent Volume Claims

- **Networking Resources**
  - Network Policies
  - Ingress rules
  - OpenShift Routes (when targeting OpenShift clusters)

- **RBAC Resources**
  - ServiceAccounts
  - Roles and ClusterRoles
  - RoleBindings and ClusterRoleBindings

- **Autoscaling Configuration**
  - Horizontal Pod Autoscalers (HPA) - v1 and v2

- **Monitoring Stack**
  - Prometheus ServiceMonitor resources
  - Ensures monitoring is deployed alongside workloads

#### Automatic Namespace Management

The operator detects when workloads require specific namespaces and automatically:
- Includes namespace definitions in the bundle
- Ensures namespaces are created before dependent resources
- Maintains proper deployment ordering

### 3. Multi-Cluster Network Connectivity Management

The operator provides intelligent network connectivity management for workloads distributed across multiple clusters.

#### External Service Analysis

- Analyzes Pod specifications to identify external service dependencies
- Detects service-to-service communication patterns
- Extracts environment variables that reference Kubernetes services

#### AC3 Network Operator Integration

Integrates with the AC3 Network Operator to enable cross-cluster service discovery:

- **Automatic Link Registration**: When workloads with external service dependencies are scheduled, the operator automatically creates MultiClusterNetwork resources
- **Network Topology Management**: Builds and maintains a graph of inter-cluster service connections
- **Lifecycle Management**: Automatically deregisters network links when workloads are deleted

#### Network Status Tracking

- Updates scheduling status to reflect network connectivity state
- Provides condition `ScheduledWithUnreliableConnectivity` when network links cannot be established
- Continues scheduling even if network operator is not installed (graceful degradation)

### 4. OCM Integration

Deep integration with Open Cluster Management for cluster federation and workload distribution.

#### Placement API

- Creates OCM Placement resources with label selectors derived from filter annotations
- Leverages OCM's cluster selection engine for intelligent placement decisions
- Respects ManagedClusterSet boundaries for cluster isolation

#### ManifestWork API

- Distributes workload bundles to target clusters using ManifestWork resources
- Each bundle is deployed as a separate ManifestWork for independent lifecycle management
- Tracks deployment status across all target clusters

#### Cluster Membership Validation

- Validates that referenced ManagedClusterSets exist
- Checks cluster set bindings to ensure namespace access
- Provides clear error conditions when cluster membership is misconfigured

### 5. Flexible Scheduling Modes

The operator supports multiple scheduling strategies to accommodate different use cases.

#### Global Scheduling Mode

Apply the same placement constraints to all manifests:

```yaml
apiVersion: scheduling.p2code.eu/v1alpha1
kind: P2CodeSchedulingManifest
metadata:
  name: global-scheduling-example
spec:
  globalAnnotations:
    - "p2code.filter.location=europe"
    - "p2code.filter.has-gpu=true"
  manifests:
    - apiVersion: apps/v1
      kind: Deployment
      # ... deployment spec
    - apiVersion: v1
      kind: Service
      # ... service spec
```

#### Per-Workload Scheduling Mode

Define placement constraints for individual workloads:

```yaml
apiVersion: scheduling.p2code.eu/v1alpha1
kind: P2CodeSchedulingManifest
metadata:
  name: per-workload-example
spec:
  workloadAnnotations:
    frontend:
      - "p2code.filter.location=europe"
    backend:
      - "p2code.filter.has-gpu=true"
      - "p2code.filter.location=greece"
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

#### Explicit Cluster Targeting

Pin workloads to specific clusters by name:

```yaml
workloadAnnotations:
  critical-workload:
    - "p2code.filter.clustername=prod-cluster-01"
```

### 6. Comprehensive Resource Type Support

The operator intelligently handles a wide range of Kubernetes and OpenShift resources.

#### Kubernetes Core Resources

- Pods, Deployments, StatefulSets, DaemonSets
- Services, Endpoints, EndpointSlices
- ConfigMaps, Secrets
- PersistentVolumes, PersistentVolumeClaims
- Jobs, CronJobs

#### Networking Resources

- NetworkPolicies
- Ingress resources
- Service mesh integrations

#### RBAC Resources

- ServiceAccounts
- Roles, ClusterRoles
- RoleBindings, ClusterRoleBindings

#### Autoscaling

- HorizontalPodAutoscaler (v1, v2, v2beta1, v2beta2)

#### OpenShift Resources

- Routes (route.openshift.io/v1)
- OpenShift-specific security constraints

#### Monitoring

- Prometheus ServiceMonitor resources (monitoring.coreos.com/v1)

### 7. Advanced Status Reporting

The operator provides detailed status information through standard Kubernetes conditions.

#### Condition Types

- **SchedulingInProgress**: Initial scheduling is underway
- **SchedulingSuccessful**: All workloads successfully scheduled
- **ScheduledWithUnreliableConnectivity**: Scheduled but network links unavailable
- **SchedulingFailed**: Scheduling failed (with detailed reason)
- **Misconfigured**: Invalid configuration detected
- **SchedulingError**: Unexpected error during scheduling

#### Scheduling Decisions

The status includes a complete record of scheduling decisions:

```yaml
status:
  decisions:
    - workload: frontend
      cluster: prod-cluster-eu-01
    - workload: backend
      cluster: prod-cluster-eu-02
  conditions:
    - type: SchedulingSuccessful
      status: "True"
      reason: AllWorkloadsScheduled
      message: "All 2 workloads successfully scheduled"
```

### 8. Lifecycle Management

#### Finalizers

The operator uses Kubernetes finalizers to ensure graceful cleanup:

- Deletes all associated ManifestWork resources from target clusters
- Removes Placement and PlacementDecision resources
- Deregisters MultiClusterNetwork links
- Ensures no orphaned resources remain after deletion

#### Ownership Tracking

All resources created by the operator are labeled with ownership information:

```yaml
labels:
  p2code.scheduler/owner: <scheduling-manifest-name>
```

This enables:
- Efficient resource lookup and cleanup
- Clear audit trail of created resources
- Prevention of resource conflicts

### 9. Validation and Error Handling

#### Pre-Scheduling Validation

- **Annotation Syntax Validation**: Ensures all filter annotations follow the `p2code.filter.x=y` format
- **Manifest Validation**: Validates that all manifests are valid Kubernetes objects
- **Cluster Membership Validation**: Verifies ManagedClusterSet existence and bindings
- **Workload Name Validation**: Ensures workload names match manifest metadata

#### Error Reporting

Clear, actionable error messages:

```yaml
conditions:
  - type: Misconfigured
    status: "True"
    reason: InvalidAnnotationFormat
    message: "Invalid annotation format: 'p2code.filter.location'. Expected 'p2code.filter.key=value'"
```

### 10. Platform-Specific Optimizations

#### OpenShift Support

- Recognizes and handles OpenShift Route resources
- Filters clusters by distribution type (OpenShift vs. vanilla Kubernetes)
- Ensures compatibility with OpenShift-specific APIs

#### Prometheus Integration

- Automatically bundles ServiceMonitor resources with their target workloads
- Ensures monitoring stack is co-located with monitored services

## Security Features

### RBAC Awareness

The operator:
- Automatically detects and includes required RBAC resources
- Ensures ServiceAccounts are created before workloads that use them
- Maintains proper RBAC boundaries across clusters

### Secret Management

- Automatically includes Secrets referenced by workloads
- Ensures secrets are created before dependent resources
- Maintains secure secret handling across cluster boundaries

## Performance Characteristics

### Efficient Reconciliation

- Uses Kubernetes controller-runtime for efficient event-driven reconciliation
- Implements proper caching to minimize API server load
- Batches operations where possible

### Resource Naming

- Automatically truncates long resource names to comply with Kubernetes limits (max 253 characters)
- Maintains readable, predictable naming conventions

## Limitations and Considerations

### Namespace Restrictions

All P2CodeSchedulingManifest resources must be created in the `p2code-scheduler-system` namespace.

### Cluster Set Requirements

Clusters must be organized into ManagedClusterSets with proper bindings to the operator namespace.

### Network Operator Dependency

Multi-cluster networking features require the AC3 Network Operator to be installed. The operator gracefully degrades if it's not available.

### OCM Dependency

The operator requires a fully functional OCM installation for cluster federation and workload distribution.

## Future Roadmap

While not currently implemented, the operator's architecture supports future enhancements such as:

- Cost-based scheduling (prefer cheaper clusters)
- Resource availability scoring
- Geographic proximity optimization for latency-sensitive workloads
- Advanced workload spreading strategies (active-active, blue-green, canary)
- Integration with additional service mesh technologies

## Summary

The P2Code Scheduler Operator provides a comprehensive, production-ready solution for multi-cluster workload scheduling in Kubernetes environments. Its annotation-based approach, intelligent bundling, and deep OCM integration make it a powerful tool for organizations managing federated Kubernetes clusters.
