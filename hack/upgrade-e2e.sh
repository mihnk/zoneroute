#!/usr/bin/env bash
# Upgrade lane: install the released v0.1.0 on a wired cluster, then apply the
# working tree over it and prove the routes people already created survive.
#
# The release asset is fetched from the immutable v0.1.0 release and checked
# against the checksum pinned below. A mismatch stops the run: the point of
# this lane is the exact artifact, so there is no fallback.
set -euo pipefail

: "${KIND:?set KIND (see Makefile)}"
: "${KIND_NODE_IMAGE:?set KIND_NODE_IMAGE (see Makefile)}"
: "${KUSTOMIZE:?set KUSTOMIZE (see Makefile)}"
: "${E2E_IMAGE:?set E2E_IMAGE (see Makefile)}"

# The exact release under test. Both values are immutable: the manifest by its
# published checksum, the controller by the digest that manifest pins.
RELEASE_VERSION=v0.1.0
RELEASE_MANIFEST_URL="https://github.com/mihnk/zoneroute/releases/download/${RELEASE_VERSION}/install.yaml"
RELEASE_MANIFEST_SHA256=450f895b9bb213e4ababb806f4c073ac1a276bdcc699d76a79a6b9787b9d4539

cd "$(dirname "$0")/.."

for tool in docker kubectl jq go curl; do
  command -v "$tool" >/dev/null || { echo "missing required tool: $tool" >&2; exit 1; }
done
docker info >/dev/null 2>&1 || { echo "docker daemon is not reachable" >&2; exit 1; }

cluster="zoneroute-upgrade-$$"
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

# The same evidence the functional lane collects, plus the CRD, which is what
# an upgrade is most likely to disturb.
diagnose() {
  echo "==> collecting diagnostics into $diag"
  kubectl get zoneroutes -o yaml >"$diag/zoneroutes.yaml" 2>&1 || true
  kubectl get crd zoneroutes.dns.mihnk.org -o yaml >"$diag/crd.yaml" 2>&1 || true
  kubectl get pods -A -o wide >"$diag/pods.txt" 2>&1 || true
  kubectl -n zoneroute-system get deployment -o yaml >"$diag/controller-deployment.yaml" 2>&1 || true
  kubectl -n zoneroute-system logs deployment/zoneroute-controller --tail=-1 >"$diag/controller.log" 2>&1 || true
  kubectl -n zoneroute-system logs deployment/zoneroute-controller --previous --tail=-1 >"$diag/controller-previous.log" 2>&1 || true
  kubectl -n kube-system get configmap coredns -o yaml >"$diag/coredns-configmap.yaml" 2>&1 || true
  kubectl -n kube-system get configmap coredns-custom -o yaml >"$diag/coredns-custom-configmap.yaml" 2>&1 \
    || echo "(coredns-custom does not exist)" >"$diag/coredns-custom-configmap.yaml"
  kubectl -n kube-system logs -l k8s-app=kube-dns --prefix --tail=-1 >"$diag/coredns.log" 2>&1 || true
  kubectl get events -A --sort-by=.lastTimestamp >"$diag/events.txt" 2>&1 || true

  echo "--- pods"; cat "$diag/pods.txt"
  echo "--- zoneroutes"; kubectl get zoneroutes 2>&1 || true
  echo "--- controller log (tail)"; tail -n 40 "$diag/controller.log"
}

echo "==> creating cluster $cluster from $KIND_NODE_IMAGE"
$KIND create cluster --name "$cluster" --image "$KIND_NODE_IMAGE" --wait 120s >/dev/null

# Built and loaded now, applied only after the release has been exercised.
echo "==> building $E2E_IMAGE from the working tree"
docker build -q -t "$E2E_IMAGE" . >/dev/null
$KIND load docker-image "$E2E_IMAGE" --name "$cluster" >/dev/null

echo "==> wiring CoreDNS"
# The same integration contract, and the same test tuning, as the functional
# lane. CoreDNS is not upgraded at any point in this scenario.
ZONEROUTE_TEST_TUNING=1 hack/wire-coredns.sh | sed 's/^/  /'

echo "==> fetching the $RELEASE_VERSION release manifest"
release_manifest="$work/install-$RELEASE_VERSION.yaml"
curl -fsSL -o "$release_manifest" "$RELEASE_MANIFEST_URL"
got=$(shasum -a 256 "$release_manifest" 2>/dev/null | awk '{print $1}' || sha256sum "$release_manifest" | awk '{print $1}')
if [ "$got" != "$RELEASE_MANIFEST_SHA256" ]; then
  echo "  FAIL  checksum mismatch for $RELEASE_MANIFEST_URL" >&2
  echo "        got      $got" >&2
  echo "        expected $RELEASE_MANIFEST_SHA256" >&2
  exit 1
fi
echo "  checksum ok: $got"

echo "==> installing $RELEASE_VERSION"
kubectl apply -f "$release_manifest" >/dev/null
kubectl wait --for=condition=Established crd/zoneroutes.dns.mihnk.org --timeout=60s >/dev/null
kubectl -n zoneroute-system rollout status deployment/zoneroute-controller --timeout=180s >/dev/null
echo "  controller image: $(kubectl -n zoneroute-system get deployment zoneroute-controller \
  -o jsonpath='{.spec.template.spec.containers[0].image}')"

echo "==> deploying test upstreams and DNS client"
coredns_image=$(kubectl -n kube-system get deployment coredns -o jsonpath='{.spec.template.spec.containers[0].image}')
sed "s|COREDNS_IMAGE|$coredns_image|g" test/e2e/manifests/upstream.yaml | kubectl apply -f - >/dev/null
kubectl -n zoneroute-e2e rollout status deployment/upstream-a --timeout=120s >/dev/null
kubectl -n zoneroute-e2e rollout status deployment/upstream-b --timeout=120s >/dev/null
kubectl -n zoneroute-e2e wait --for=condition=Ready pod/dig --timeout=180s >/dev/null

echo "==> running the upgrade scenario"
# The Go test performs the upgrade itself, through ZONEROUTE_UPGRADE_APPLY,
# so the assertions either side of it live in one place.
export ZONEROUTE_UPGRADE_APPLY="$KUSTOMIZE build test/e2e/overlay"
go test -tags e2e_upgrade -count=1 -failfast -timeout 30m -v ./test/e2e/
status=0
echo "==> upgrade e2e passed"
