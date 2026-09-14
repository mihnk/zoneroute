#!/usr/bin/env bash
# Installs the CRD on a throwaway Kubernetes cluster and checks that the API
# server enforces the schema and CEL rules. This is API conformance, not the
# functional end-to-end suite.
set -euo pipefail

: "${KIND:?set KIND (see Makefile)}"
: "${KIND_NODE_IMAGE:?set KIND_NODE_IMAGE (see Makefile)}"

cd "$(dirname "$0")/.."

cluster=zoneroute-crd-verify
ctx="kind-$cluster"
data=hack/testdata/crd
work=hack/.verify-crd
mkdir -p "$work"

cleanup() { $KIND delete cluster --name "$cluster" >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo "==> creating cluster from $KIND_NODE_IMAGE"
$KIND create cluster --name "$cluster" --image "$KIND_NODE_IMAGE" --wait 120s >/dev/null

k() { kubectl --context "$ctx" "$@"; }

echo "==> installing CRD"
# The file, not the directory: config/crd also holds the kustomization
# that config/default builds on, and that is not a cluster resource.
k apply -f config/crd/dns.mihnk.org_zoneroutes.yaml >/dev/null
k wait --for=condition=Established crd/zoneroutes.dns.mihnk.org --timeout=60s >/dev/null

fail=0
pass() { echo "  ok    $1"; }
flunk() { echo "  FAIL  $1: $2"; fail=1; }

# expect_accept FILE — server-side dry run must succeed.
expect_accept() {
  if k apply --dry-run=server -f "$data/$1" -o yaml >"$work/$1.out" 2>"$work/$1.err"; then
    pass "$1 accepted"
  else
    flunk "$1" "rejected: $(tr '\n' ' ' <"$work/$1.err")"
  fi
}

# expect_reject FILE SUBSTRING — server-side dry run must fail, mentioning SUBSTRING.
expect_reject() {
  if k apply --dry-run=server -f "$data/$1" >/dev/null 2>"$work/$1.err"; then
    flunk "$1" "accepted, expected rejection"
  elif grep -q -- "$2" "$work/$1.err"; then
    pass "$1 rejected ($2)"
  else
    flunk "$1" "rejected for the wrong reason: $(tr '\n' ' ' <"$work/$1.err")"
  fi
}

# expect_field FILE JSONPATH WANT — server-side dry run output must match.
expect_field() {
  got=$(k apply --dry-run=server -f "$data/$1" -o jsonpath="$2" 2>"$work/$1.err" || true)
  if [ "$got" = "$3" ]; then
    pass "$1 $2 = $3"
  else
    flunk "$1" "$2 = '$got', want '$3'"
  fi
}

echo "==> exercising validation"
expect_accept valid.yaml
expect_reject dup-normalized-zones.yaml "unique ignoring case"
expect_reject reserved-zone.yaml        "reserved"
expect_reject bad-ip.yaml               "canonical IPv4 or IPv6"
expect_reject noncanonical-ip.yaml      "canonical IPv4 or IPv6"
expect_reject dup-upstreams.yaml        "unique by address and port"
expect_field  port-default.yaml '{.spec.upstreams[0].port}' 53
expect_field  ordered-upstreams.yaml '{.spec.upstreams[*].address}' "10.0.0.3 10.0.0.1 10.0.0.2"

if [ "$fail" -ne 0 ]; then
  echo "==> FAILED"
  exit 1
fi
echo "==> all checks passed"
