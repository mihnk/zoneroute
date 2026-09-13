#!/usr/bin/env bash
# Functional end-to-end suite: a throwaway kind cluster on the pinned
# Kubernetes 1.31 image, the stock kubeadm CoreDNS wired to the integration
# contract, the controller installed from the production manifests, and the
# scenarios in test/e2e run against real DNS. Set KEEP_E2E_CLUSTER=1 to keep
# the cluster for debugging.
set -euo pipefail

: "${KIND:?set KIND (see Makefile)}"
: "${KIND_NODE_IMAGE:?set KIND_NODE_IMAGE (see Makefile)}"
: "${KUSTOMIZE:?set KUSTOMIZE (see Makefile)}"
: "${E2E_IMAGE:?set E2E_IMAGE (see Makefile)}"

cd "$(dirname "$0")/.."

for tool in docker kubectl jq go; do
  command -v "$tool" >/dev/null || { echo "missing required tool: $tool" >&2; exit 1; }
done
docker info >/dev/null 2>&1 || { echo "docker daemon is not reachable" >&2; exit 1; }

cluster="zoneroute-e2e-$$"
work=hack/.e2e
diag="$work/diag"
rm -rf "$work"
mkdir -p "$diag"
export KUBECONFIG="$PWD/$work/kubeconfig"

status=1
cleanup() {
  if [ "$status" -ne 0 ]; then diagnose || true; fi
  if [ "${KEEP_E2E_CLUSTER:-}" = "1" ]; then
    echo "==> keeping cluster $cluster; export KUBECONFIG=$KUBECONFIG"
  else
    echo "==> deleting cluster $cluster"
    $KIND delete cluster --name "$cluster" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

# diagnose writes the cluster state that explains a failure into $diag and
# prints a short summary. No Secrets are read.
diagnose() {
  echo "==> collecting diagnostics into $diag"
  kubectl get zoneroutes -o yaml >"$diag/zoneroutes.yaml" 2>&1 || true
  kubectl get pods -A -o wide >"$diag/pods.txt" 2>&1 || true
  kubectl -n zoneroute-system get deployment -o wide >"$diag/controller-deployment.txt" 2>&1 || true
  kubectl -n kube-system get configmap coredns -o yaml >"$diag/coredns-configmap.yaml" 2>&1 || true
  kubectl -n kube-system get configmap coredns-custom -o yaml >"$diag/coredns-custom-configmap.yaml" 2>&1 \
    || echo "(coredns-custom does not exist)" >"$diag/coredns-custom-configmap.yaml"
  kubectl -n zoneroute-system logs deployment/zoneroute-controller --tail=-1 >"$diag/controller.log" 2>&1 || true
  kubectl -n zoneroute-system logs deployment/zoneroute-controller --previous --tail=-1 >"$diag/controller-previous.log" 2>&1 || true
  kubectl -n kube-system logs -l k8s-app=kube-dns --prefix --tail=-1 >"$diag/coredns.log" 2>&1 || true
  kubectl -n zoneroute-e2e logs -l app.kubernetes.io/part-of=zoneroute-e2e --prefix --tail=-1 >"$diag/upstreams.log" 2>&1 || true
  kubectl get events -A --sort-by=.lastTimestamp >"$diag/events.txt" 2>&1 || true
  # Describe only what is unhealthy.
  kubectl -n zoneroute-system describe deployment/zoneroute-controller >"$diag/controller-describe.txt" 2>&1 || true
  kubectl get pods -A --field-selector=status.phase!=Running -o name 2>/dev/null \
    | while read -r p; do kubectl describe "$p" 2>&1 || true; echo; done >"$diag/unhealthy-pods.txt" || true

  echo "--- pods"; cat "$diag/pods.txt"
  echo "--- zoneroutes"; kubectl get zoneroutes 2>&1 || true
  echo "--- controller log (tail)"; tail -n 40 "$diag/controller.log"
}

echo "==> creating cluster $cluster from $KIND_NODE_IMAGE"
$KIND create cluster --name "$cluster" --image "$KIND_NODE_IMAGE" --wait 120s >/dev/null

echo "==> building $E2E_IMAGE"
docker build -q -t "$E2E_IMAGE" . >/dev/null

echo "==> loading image into the cluster"
$KIND load docker-image "$E2E_IMAGE" --name "$cluster" >/dev/null

echo "==> wiring CoreDNS"
hack/wire-coredns.sh | sed 's/^/  /'

echo "==> installing ZoneRoute from the production manifests"
$KUSTOMIZE build test/e2e/overlay | kubectl apply -f - >/dev/null
kubectl wait --for=condition=Established crd/zoneroutes.dns.mihnk.org --timeout=60s >/dev/null
kubectl -n zoneroute-system rollout status deployment/zoneroute-controller --timeout=120s >/dev/null

echo "==> deploying test upstreams and DNS client"
coredns_image=$(kubectl -n kube-system get deployment coredns -o jsonpath='{.spec.template.spec.containers[0].image}')
echo "  upstream image (preloaded on the node): $coredns_image"
sed "s|COREDNS_IMAGE|$coredns_image|g" test/e2e/manifests/upstream.yaml | kubectl apply -f - >/dev/null
kubectl -n zoneroute-e2e rollout status deployment/upstream-a --timeout=120s >/dev/null
kubectl -n zoneroute-e2e rollout status deployment/upstream-b --timeout=120s >/dev/null
kubectl -n zoneroute-e2e wait --for=condition=Ready pod/dig --timeout=180s >/dev/null

echo "==> running scenarios"
go test -tags e2e -count=1 -failfast -timeout 30m -v ./test/e2e/
status=0
echo "==> e2e passed"
