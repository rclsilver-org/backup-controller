#!/bin/bash

set -e

# creating the kind cluster
test -d ${HOME}/.kube || mkdir -p ${HOME}/.kube
kind get kubeconfig >/${HOME}/.kube/config || kind create cluster

# helm
function helm_repo_add() {
    if [ $(helm repo list -o json | jq "[.[] | select(.name == \"${1}\")] | length") -eq 0 ]; then
        helm repo add ${1} ${2}
    fi
}
helm_repo_add bitnami https://charts.bitnami.com/bitnami
helm repo update

# install cert-manager
CM_VERSION=v1.16.3
CM_IMAGE_BASE=quay.io/jetstack/cert-manager
kubectl get crd certificates.cert-manager.io || kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/${CM_VERSION}/cert-manager.yaml
for CM_IMAGE_SUFFIX in webhook cainjector controller
do
    docker pull ${CM_IMAGE_BASE}-${CM_IMAGE_SUFFIX}:${CM_VERSION}
    kind load docker-image ${CM_IMAGE_BASE}-${CM_IMAGE_SUFFIX}:${CM_VERSION}
done 

# default resources
make install
