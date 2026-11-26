#!/usr/bin/env bash
# Regression test: changing the image of a GPU workload must finish even when every
# GPU in the cluster is in use. Assumes `make demo` (or kind-gpus plus a Helm install)
# has run on a 1-node Kind cluster with 4 fake GPUs.
#
#   1. A workload of 2 pods x 2 GPUs takes all 4 GPUs and goes Running.
#   2. Its image is changed. The Deployment default would surge a third pod, which
#      cannot be placed, so the rollout would hang. The operator replaces one pod at
#      a time instead, so the rollout has to finish.
set -euo pipefail

NEW_IMAGE="nginx:1.27-alpine"

kubectl delete gpuworkloads --all --wait

echo ">> 1. workload takes all 4 GPUs"
kubectl apply -f - <<YAML
apiVersion: gpu.kube-op-lite.io/v1alpha1
kind: GpuWorkload
metadata:
  name: rollout-test
spec:
  replicas: 2
  image: nginx:1.27
  gpuCount: 2
  priority: medium
YAML
kubectl wait gpuworkload/rollout-test --for=jsonpath='{.status.phase}'=Running --timeout=180s

echo ">> 2. change the image while no GPU is free"
kubectl patch gpuworkload rollout-test --type=merge -p "{\"spec\":{\"image\":\"${NEW_IMAGE}\"}}"
if ! kubectl rollout status deploy/rollout-test --timeout=120s; then
  echo "FAIL: the rollout did not finish"
  kubectl get pods -l gpu.kube-op-lite.io/workload=rollout-test
  exit 1
fi

images=$(kubectl get pods -l gpu.kube-op-lite.io/workload=rollout-test \
  -o jsonpath='{range .items[*]}{.spec.containers[0].image}{"\n"}{end}' | sort -u)
if [ "$images" != "$NEW_IMAGE" ]; then
  echo "FAIL: pods still run: $images"
  exit 1
fi

echo ">> OK: every pod runs ${NEW_IMAGE}"
kubectl delete gpuworkload rollout-test --wait
