#!/usr/bin/env bash
# Wires a kubeadm-style CoreDNS to the ZoneRoute integration contract:
#
#   1. kube-system/coredns-custom exists;
#   2. the Corefile has a top-level `import custom/*.server`;
#   3. coredns-custom is mounted at /etc/coredns/custom (optional, so the
#      ConfigMap may be absent without breaking CoreDNS).
#
# Idempotent: each step is skipped when already satisfied. Everything else in
# the Corefile and in the CoreDNS Deployment is left untouched. Requires
# kubectl and jq, and honours KUBECONFIG.
set -euo pipefail

ns=kube-system
import='import custom/*.server'
k() { kubectl -n "$ns" "$@"; }

echo "==> coredns-custom ConfigMap"
if k get configmap coredns-custom >/dev/null 2>&1; then
  echo "  present"
else
  k create configmap coredns-custom >/dev/null
  echo "  created"
fi

echo "==> Corefile import"
corefile=$(k get configmap coredns -o jsonpath='{.data.Corefile}')
if grep -qE '^[[:space:]]*import custom/\*\.server[[:space:]]*$' <<<"$corefile"; then
  echo "  present"
else
  patch=$(jq -n --arg c "$corefile"$'\n'"$import"$'\n' '{data: {Corefile: $c}}')
  k patch configmap coredns --type merge -p "$patch" >/dev/null
  echo "  added"
fi

echo "==> coredns-custom mount"
if k get deployment coredns -o jsonpath='{.spec.template.spec.volumes[*].name}' | tr ' ' '\n' | grep -qx custom-config-volume; then
  echo "  present"
else
  # Strategic merge: volumes and containers merge by name, volumeMounts by
  # mountPath, so existing entries are preserved.
  k patch deployment coredns --type strategic -p '{
    "spec": {"template": {"spec": {
      "volumes": [{"name": "custom-config-volume", "configMap": {"name": "coredns-custom", "optional": true}}],
      "containers": [{"name": "coredns", "volumeMounts": [{"name": "custom-config-volume", "mountPath": "/etc/coredns/custom", "readOnly": true}]}]
    }}}}' >/dev/null
  echo "  added"
fi

echo "==> waiting for CoreDNS"
k rollout status deployment/coredns --timeout=120s >/dev/null
echo "  ready"
