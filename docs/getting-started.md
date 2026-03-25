# Getting Started Guide

This guide will walk you through deploying and using the P2Code Scheduler Operator in your Kubernetes environment.

## Prerequisites

Before you begin, ensure you have the following:

### Required Software

- **Go**: v1.22.0 or later
- **Docker**: v17.03 or later
- **kubectl**: v1.11.3 or later
- **Kubernetes cluster**: v1.11.3 or later with admin access

### Required Infrastructure

- **Open Cluster Management (OCM)**: Must be installed and configured
- **Managed Clusters**: At least one cluster registered with OCM
- **ManagedClusterSet**: Configured with proper bindings

### Optional Dependencies

- **AC3 MultiClusterNetwork Operator**: For multi-cluster network connectivity features. Installation guide available [here].(https://github.com/rh-waterford-et/ac3_networkoperator)
- **Prometheus Operator**: For ServiceMonitor support

## Installation

### Step 1: Install Open Cluster Management

If OCM is not already installed, follow the [OCM installation guide](https://open-cluster-management.io/getting-started/installation/).

Quick install for testing:

```bash
# Install OCM hub components
curl -L https://raw.githubusercontent.com/open-cluster-management-io/OCM/main/deploy/hub-registration-operator.yaml | kubectl apply -f -

# Wait for hub to be ready
kubectl wait --for=condition=Ready pod -l app=clustermanager -n open-cluster-management --timeout=300s
```

### Step 2: Register Managed Clusters

Register your managed clusters with OCM. For each cluster:

```bash
# Install klusterlet on managed cluster
clusteradm join --hub-token <token> --hub-apiserver <hub-apiserver-url> --cluster-name <cluster-name>

# Accept the cluster on hub
clusteradm accept --clusters <cluster-name>
```

### Step 3: Create ManagedClusterSet

User the ```clusteradm``` CLI to configure the managed cluster sets

- ```clusteradm create clusterset test```
- ```clusteradm clusterset set test --clusters cluster1```
- ```clusteradm clusterset set test --clusters cluster2```

Bind the clusterset to the p2code-schdeuler-system namespace
- ```clusteradm clusterset bind test --namespace p2code-scheduler-system```

### Step 4: Label Your Managed Clusters

On each ManagedCluster create a ```ClusterClaim``` to express any cluster property.

Example ClusterClaim

```yaml
apiVersion: cluster.open-cluster-management.io/v1alpha1
kind: ClusterClaim
metadata:
  name: p2code.filter.k8sdistribution
spec:
  value: kubernetes
```

### Step 5: Install the P2Code Scheduler Operator

#### Option A: Install from Source

```bash
# Clone the repository
git clone https://github.com/your-org/p2code_scheduler_operator.git
cd p2code_scheduler_operator

# Install CRDs
make install

# Build and push the operator image
make docker-build docker-push IMG=<your-registry>/p2code-scheduler:latest

# Deploy the operator
make deploy IMG=<your-registry>/p2code-scheduler:latest
```

#### Option B: Install from Release

```bash
# Install using the pre-built installer
kubectl apply -f https://github.com/your-org/p2code_scheduler_operator/releases/latest/download/install.yaml
```

### Step 6: Verify Installation

Check that the operator is running:

```bash
# Check operator pod status
kubectl get pods -n p2code-scheduler-system

# Expected output:
# NAME                                                   READY   STATUS    RESTARTS   AGE
# p2code-scheduler-controller-manager-xxxxxxxxxx-xxxxx   2/2     Running   0          1m

# Verify CRD installation
kubectl get crd p2codeschedulingmanifests.scheduling.p2code.eu

# Check operator logs
kubectl logs -n p2code-scheduler-system deployment/p2code-scheduler-controller-manager -c manager
```

## Quick Start Examples

### Example 1: Simple Global Scheduling

Schedule a deployment to all clusters in Europe:

```bash
cat <<EOF | kubectl apply -f -
apiVersion: scheduling.p2code.eu/v1alpha1
kind: P2CodeSchedulingManifest
metadata:
  name: simple-deployment
  namespace: p2code-scheduler-system
spec:
  globalAnnotations:
    - "p2code.target.managedClusterSet=test"
    - "p2code.filter.location=europe"
  manifests:
    - apiVersion: apps/v1
      kind: Deployment
      metadata:
        name: nginx
        namespace: default
      spec:
        replicas: 2
        selector:
          matchLabels:
            app: nginx
        template:
          metadata:
            labels:
              app: nginx
          spec:
            containers:
              - name: nginx
                image: nginx:latest
                ports:
                  - containerPort: 80
    - apiVersion: v1
      kind: Service
      metadata:
        name: nginx
        namespace: default
      spec:
        selector:
          app: nginx
        ports:
          - port: 80
            targetPort: 80
EOF
```

Check the scheduling status:

```bash
# View the scheduling manifest
kubectl get p2codeschedulingmanifest simple-deployment -n p2code-scheduler-system -o yaml

# Check scheduling decisions
kubectl get p2codeschedulingmanifest simple-deployment -n p2code-scheduler-system -o jsonpath='{.status.decisions}'

# View created placements
kubectl get placements -n p2code-scheduler-system

# View created manifestworks
kubectl get manifestworks --all-namespaces
```

### Example 2: Per-Workload Scheduling

Schedule different workloads to different clusters:

```bash
cat <<EOF | kubectl apply -f -
apiVersion: scheduling.p2code.eu/v1alpha1
kind: P2CodeSchedulingManifest
metadata:
  name: multi-workload
  namespace: p2code-scheduler-system
spec:
  globalAnnotations:
    - "p2code.target.managedClusterSet=test"
  workloadAnnotations:
    frontend:
      - "p2code.filter.location=europe"
      - "p2code.filter.distribution=kubernetes"
    backend:
      - "p2code.filter.has-gpu=true"
  manifests:
    - apiVersion: apps/v1
      kind: Deployment
      metadata:
        name: frontend
        namespace: default
      spec:
        replicas: 3
        selector:
          matchLabels:
            app: frontend
        template:
          metadata:
            labels:
              app: frontend
          spec:
            containers:
              - name: nginx
                image: nginx:latest
                ports:
                  - containerPort: 80
    - apiVersion: v1
      kind: Service
      metadata:
        name: frontend
        namespace: default
      spec:
        selector:
          app: frontend
        ports:
          - port: 80
    - apiVersion: apps/v1
      kind: Deployment
      metadata:
        name: backend
        namespace: default
      spec:
        replicas: 2
        selector:
          matchLabels:
            app: backend
        template:
          metadata:
            labels:
              app: backend
          spec:
            containers:
              - name: ml-service
                image: tensorflow/serving:latest
                ports:
                  - containerPort: 8501
                resources:
                  limits:
                    nvidia.com/gpu: 1
    - apiVersion: v1
      kind: Service
      metadata:
        name: backend
        namespace: default
      spec:
        selector:
          app: backend
        ports:
          - port: 8501
EOF
```

### Example 3: Complete Application Stack

Deploy a full application with dependencies:

```bash
cat <<EOF | kubectl apply -f -
apiVersion: scheduling.p2code.eu/v1alpha1
kind: P2CodeSchedulingManifest
metadata:
  name: full-stack-app
  namespace: p2code-scheduler-system
spec:
  globalAnnotations:
    - "p2code.target.managedClusterSet=test"
    - "p2code.filter.location=europe"
  manifests:
    # Namespace
    - apiVersion: v1
      kind: Namespace
      metadata:
        name: myapp

    # ConfigMap
    - apiVersion: v1
      kind: ConfigMap
      metadata:
        name: app-config
        namespace: myapp
      data:
        database.url: "postgresql://db:5432/myapp"

    # Secret
    - apiVersion: v1
      kind: Secret
      metadata:
        name: app-secrets
        namespace: myapp
      type: Opaque
      data:
        db-password: "changeme"

    # Service Account
    - apiVersion: v1
      kind: ServiceAccount
      metadata:
        name: app-sa
        namespace: myapp

    # Deployment
    - apiVersion: apps/v1
      kind: Deployment
      metadata:
        name: web-app
        namespace: myapp
      spec:
        replicas: 2
        selector:
          matchLabels:
            app: web-app
        template:
          metadata:
            labels:
              app: web-app
          spec:
            serviceAccountName: app-sa
            containers:
              - name: app
                image: myapp:v1.0.0
                ports:
                  - containerPort: 8080
                envFrom:
                  - configMapRef:
                      name: app-config
                  - secretRef:
                      name: app-secrets

    # Service
    - apiVersion: v1
      kind: Service
      metadata:
        name: web-app
        namespace: myapp
      spec:
        selector:
          app: web-app
        ports:
          - port: 80
            targetPort: 8080
        type: LoadBalancer

    # HPA
    - apiVersion: autoscaling/v2
      kind: HorizontalPodAutoscaler
      metadata:
        name: web-app-hpa
        namespace: myapp
      spec:
        scaleTargetRef:
          apiVersion: apps/v1
          kind: Deployment
          name: web-app
        minReplicas: 2
        maxReplicas: 10
        metrics:
          - type: Resource
            resource:
              name: cpu
              target:
                type: Utilization
                averageUtilization: 70
EOF
```

## Monitoring and Troubleshooting

### Check Scheduling Status

```bash
# View all scheduling manifests
kubectl get p2codeschedulingmanifests -n p2code-scheduler-system

# Describe a specific manifest
kubectl describe p2codeschedulingmanifest <name> -n p2code-scheduler-system

# View detailed status
kubectl get p2codeschedulingmanifest <name> -n p2code-scheduler-system -o yaml
```

### Common Status Conditions

```yaml
# Successful scheduling
conditions:
  - type: SchedulingSuccessful
    status: "True"
    reason: AllWorkloadsScheduled
    message: "All 2 workloads successfully scheduled"

# Scheduling in progress
conditions:
  - type: SchedulingInProgress
    status: "True"
    reason: PlacementPending
    message: "Waiting for placement decisions"

# Configuration error
conditions:
  - type: Misconfigured
    status: "True"
    reason: InvalidAnnotationFormat
    message: "Annotation 'p2code.filter.location' is missing value"

# Scheduling failure
conditions:
  - type: SchedulingFailed
    status: "True"
    reason: NoMatchingClusters
    message: "No clusters match the filter criteria"
```

### View Placement Decisions

```bash
# List all placements
kubectl get placements -n p2code-scheduler-system

# View placement decisions
kubectl get placementdecisions -n p2code-scheduler-system

# Describe a specific placement
kubectl describe placement <placement-name> -n p2code-scheduler-system
```

### Check ManifestWork Status

```bash
# List all manifestworks across clusters
kubectl get manifestworks --all-namespaces

# View manifestwork details
kubectl describe manifestwork <manifestwork-name> -n <cluster-namespace>

# Check manifestwork status
kubectl get manifestwork <manifestwork-name> -n <cluster-namespace> -o jsonpath='{.status.conditions}'
```

### View Operator Logs

```bash
# Follow operator logs
kubectl logs -n p2code-scheduler-system deployment/p2code-scheduler-controller-manager -c manager -f

# View recent logs
kubectl logs -n p2code-scheduler-system deployment/p2code-scheduler-controller-manager -c manager --tail=100

# Search for errors
kubectl logs -n p2code-scheduler-system deployment/p2code-scheduler-controller-manager -c manager | grep ERROR
```

### Debugging Common Issues

#### Issue: No clusters selected

**Symptoms**: Status shows "No matching clusters"

**Solution**:
1. Verify cluster labels match your filter annotations. Run the ```list-labeled-cluster.sh``` script to view the ```p2code``` labels assigned to each ```ManagedCluster```.
2. Check that ManagedClusterSet is properly configured
3. Verify ManagedClusterSetBinding exists in `p2code-scheduler-system` namespace

#### Issue: ManifestWork not created

**Symptoms**: Placement exists but no ManifestWork resources

**Solution**:
1. Check PlacementDecision status:
   ```bash
   kubectl get placementdecisions -n p2code-scheduler-system -o yaml
   ```
2. Verify operator has proper RBAC permissions
3. Check operator logs for errors

#### Issue: Workload not deployed to cluster

**Symptoms**: ManifestWork created but workload not running on target cluster

**Solution**:
1. Check ManifestWork status on hub cluster
2. Check ManifestWork agent logs on managed cluster:
   ```bash
   kubectl logs -n open-cluster-management-agent deployment/klusterlet-work-agent
   ```
3. Verify network connectivity between hub and managed cluster

#### Issue: Invalid annotation format

**Symptoms**: Status condition shows "InvalidAnnotationFormat"

**Solution**:
Ensure annotations follow the format `p2code.filter.key=value`:

```yaml
# Correct
globalAnnotations:
  - "p2code.filter.location=europe"

# Incorrect
globalAnnotations:
  - "p2code.filter.location"  # Missing value
  - "location=europe"          # Missing prefix
```

## Advanced Usage

### Using Multiple Filter Annotations

Combine multiple filters for precise cluster selection:

```yaml
spec:
  globalAnnotations:
    - "p2code.filter.location=europe"
    - "p2code.filter.has-gpu=true"
    - "p2code.filter.distribution=kubernetes"
```

This will select clusters that have ALL three labels.

### Targeting Specific Clusters

Pin a workload to a specific cluster:

```yaml
spec:
  workloadAnnotations:
    critical-app:
      - "p2code.filter.clustername=prod-cluster-01"
```

### Working with OpenShift Routes

The operator automatically handles OpenShift Routes when targeting OpenShift clusters:

```yaml
manifests:
  - apiVersion: route.openshift.io/v1
    kind: Route
    metadata:
      name: my-app
      namespace: default
    spec:
      to:
        kind: Service
        name: my-app
      port:
        targetPort: 8080
```

### Enabling Multi-Cluster Networking

If you have the AC3 MultiClusterNetwork Operator installed, the P2Code Scheduler will automatically register network connections between workloads:

```yaml
spec:
  workloadAnnotations:
    frontend:
      - "p2code.filter.location=europe"
    backend:
      - "p2code.filter.location=us"
  manifests:
    - apiVersion: apps/v1
      kind: Deployment
      metadata:
        name: frontend
      spec:
        template:
          spec:
            containers:
              - name: app
                env:
                  - name: BACKEND_URL
                    value: "http://backend:8080"  # Triggers network link creation
    - apiVersion: apps/v1
      kind: Deployment
      metadata:
        name: backend
    - apiVersion: v1
      kind: Service
      metadata:
        name: backend
      spec:
        ports:
          - port: 8080
```

The operator will create a MultiClusterNetwork resource to enable cross-cluster communication.

## Cleanup

### Delete a Scheduling Manifest

```bash
kubectl delete p2codeschedulingmanifest <name> -n p2code-scheduler-system
```

This will automatically:
- Delete all ManifestWork resources from target clusters
- Remove Placement resources
- Deregister network links
- Clean up workloads from managed clusters

### Uninstall the Operator

```bash
# Delete all scheduling manifests first
kubectl delete p2codeschedulingmanifests --all -n p2code-scheduler-system

# Uninstall the operator
make undeploy

# Remove CRDs
make uninstall
```

## Best Practices

### 1. Namespace Management

Always specify namespaces in your manifests:

```yaml
# Good
metadata:
  name: my-app
  namespace: production

Scheduling will fail if a namespace isnt specified for a namespaced resource.
```

### 2. Resource Naming

Use clear, descriptive names for workloads:

```yaml
# Good
metadata:
  name: user-authentication-service

# Avoid
metadata:
  name: svc1
```

### 3. Label Organization

Organize cluster labels hierarchically:

```yaml
# Good
p2code.filter.region=europe
p2code.filter.region.country=germany
p2code.filter.region.country.city=berlin

# Also good
p2code.filter.environment=production
p2code.filter.tier=premium
```

### 4. Start Simple

Begin with global annotations and add per-workload annotations as needed:

```yaml
# Start here
spec:
  globalAnnotations:
    - "p2code.filter.location=europe"
  manifests: [...]

# Add complexity only when needed
spec:
  workloadAnnotations:
    frontend:
      - "p2code.filter.location=europe"
    backend:
      - "p2code.filter.has-gpu=true"
  manifests: [...]
```

### 5. Monitor Status

Always check the status after creating a scheduling manifest:

```bash
kubectl get p2codeschedulingmanifest <name> -n p2code-scheduler-system -o jsonpath='{.status.state} | jq
```

### 6. Use Version Control

Store your P2CodeSchedulingManifest resources in Git for reproducibility and audit trails.

### 7. Test in Non-Production First

Always test scheduling logic in a non-production environment before deploying to production clusters.

## Next Steps

- Read the [Features documentation](features.md) for detailed feature descriptions
- Review the [Architecture documentation](architecture.md) to understand how the operator works
- Explore the [example manifests](../config/samples/) in the repository
- Join the community and contribute to the project

## Getting Help

If you encounter issues:

1. Check the [troubleshooting section](#monitoring-and-troubleshooting) above
2. Review operator logs for error messages
3. Search existing GitHub issues
4. Open a new issue with:
   - P2CodeSchedulingManifest YAML
   - Operator logs
   - Cluster configuration details
   - Expected vs. actual behavior

## Summary

You now have the P2Code Scheduler Operator installed and know how to:
- Create scheduling manifests with filter annotations
- Monitor scheduling status
- Troubleshoot common issues
- Use advanced features like per-workload scheduling

Start with simple examples and gradually adopt more advanced features as your needs grow.
