#!/bin/bash

# This script lists any p2code label assigned to a ManagedCluster registered to the Hub cluster from where the scheduler runs
# These labels can be used as a target in the globalAnnotations or workloadAnnotations section of the P2CodeSchedulingManifest

# This script should be run from a hub cluster with the ManagedCluster resource installed
# jq must be installed for this script to extract and manipulate data

managedClusters=$(kubectl get managedclusters --output=jsonpath='{range .items[*]}{.metadata.name}{" "}{end}') 

if [ $? -ne 0 ]; then
    exit 1
fi

for mc in $managedClusters
do
    # Skip for the hub cluster which is referred to as the local-cluster
    if [ $mc == "local-cluster" ]; then
        continue
    fi

    echo -e "\033[1;4m$mc\033[0m"
    kubectl get managedclusters $mc -o json | jq -r '.status.clusterClaims[]| select(.name | test("p2code.filter")) | "\(.name): \(.value)"' 
done