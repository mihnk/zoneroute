#!/usr/bin/env bash
# Applies install.yaml to a throwaway Kubernetes cluster and checks that the
# API server accepts every object. This is manifest validation only: the
# controller image is not published yet, so the Deployment is not expected
# to become Ready. The functional suite is issue #8.
set -euo pipefail

: "${KIND:?set KIND (see Makefile)}"
: "${KIND_NODE_IMAGE:?set KIND_NODE_IMAGE (see Makefile)}"

cd "$(dirname "$0")/.."

cluster=zoneroute-install-verify
ctx="kind-$cluster"
work=hack/.verify-install
mkdir -p "$work"

cleanup() { $KIND delete cluster --name "$cluster" >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo "==> creating cluster from $KIND_NODE_IMAGE"
$KIND create cluster --name "$cluster" --image "$KIND_NODE_IMAGE" --wait 120s >/dev/null

k() { kubectl --context "$ctx" "$@"; }

# A server-side dry run of the whole bundle cannot work before the Namespace
# exists, so the bundle is applied for real; the API server validates each
# object on the way in. Warnings (e.g. PodSecurity) are treated as failures.
echo "==> applying install.yaml"
k apply -f install.yaml >"$work/apply.out" 2>"$work/apply.err"
if [ -s "$work/apply.err" ]; then
  echo "  FAIL  apply produced warnings or errors:"
  cat "$work/apply.err"
  exit 1
fi
cat "$work/apply.out" | sed 's/^/  /'

echo "==> checking the applied objects"
k wait --for=condition=Established crd/zoneroutes.dns.mihnk.org --timeout=60s >/dev/null
k -n zoneroute-system get serviceaccount/zoneroute-controller deployment/zoneroute-controller >/dev/null
k -n zoneroute-system get role/zoneroute-leader-election rolebinding/zoneroute-leader-election >/dev/null
k -n kube-system get role/zoneroute-coredns rolebinding/zoneroute-coredns >/dev/null
k get clusterrole/zoneroute-controller clusterrolebinding/zoneroute-controller >/dev/null

echo "==> re-applying is a no-op"
k apply -f install.yaml | grep -v unchanged && { echo "  FAIL  second apply changed something"; exit 1; }

echo "==> all checks passed"
