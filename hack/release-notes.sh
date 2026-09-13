#!/usr/bin/env bash
# Builds the GitHub Release body for VERSION. The prose comes from that
# version's section of CHANGELOG.md — the single reviewable source — and this
# script appends only what is known at release time: the exact asset URL and
# the published image digest. Prints to stdout.
#
#   VERSION=vX.Y.Z            required
#   DIGEST=sha256:...         optional; omitted in a dry run
set -euo pipefail

: "${VERSION:?set VERSION}"
digest="${DIGEST:-}"

cd "$(dirname "$0")/.."

repo=ghcr.io/mihnk/zoneroute
url="https://github.com/mihnk/zoneroute/releases/download/$VERSION/install.yaml"

# The section runs from its own heading to the next "## " heading.
notes=$(awk -v want="## $VERSION" '
  $0 == want {found = 1; next}
  found && /^## / {exit}
  found {print}
' CHANGELOG.md)

if [ -z "$(printf '%s' "$notes" | tr -d '[:space:]')" ]; then
  echo "CHANGELOG.md has no section for $VERSION" >&2
  exit 1
fi

printf '%s\n' "$notes"

cat <<EOF

## Install

\`\`\`sh
kubectl apply -f $url
\`\`\`

The manifest installs the CRD, the \`zoneroute-system\` namespace, RBAC and the
controller. It does **not** create \`kube-system/coredns-custom\` and does not
touch CoreDNS; establish the integration contract once, as described in
[docs/coredns-wiring.md](https://github.com/mihnk/zoneroute/blob/$VERSION/docs/coredns-wiring.md).

## Image
EOF

if [ -n "$digest" ]; then
  cat <<EOF

\`\`\`
$repo:$VERSION
$repo@$digest
\`\`\`

Platforms: linux/amd64, linux/arm64. The installation manifest above pins the
digest, not the tag. Build provenance and an SBOM are attached to the image;
verify the provenance with:

\`\`\`sh
gh attestation verify oci://$repo@$digest --repo mihnk/zoneroute
\`\`\`
EOF
else
  printf '\n```\n%s:%s\n```\n' "$repo" "$VERSION"
fi
