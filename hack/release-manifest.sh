#!/usr/bin/env bash
# Renders the release installation manifest: the bundle from config/default
# with the controller image replaced by the released one. Prints to stdout.
#
#   DIGEST=sha256:...  the published multi-platform index digest (preferred:
#                      the manifest then names the exact image bytes)
#   VERSION=vX.Y.Z     used when DIGEST is empty, for dry runs
#
# The repository's committed install.yaml is the development manifest and is
# never rewritten by a release.
set -euo pipefail

: "${KUSTOMIZE:?set KUSTOMIZE (see Makefile)}"

repo=ghcr.io/mihnk/zoneroute
version="${VERSION:-}"
digest="${DIGEST:-}"

if [ -z "$digest" ] && [ -z "$version" ]; then
  echo "set DIGEST (preferred) or VERSION" >&2
  exit 1
fi
if [ -n "$digest" ] && [[ ! "$digest" =~ ^sha256:[0-9a-f]{64}$ ]]; then
  echo "DIGEST must look like sha256:<64 hex>, got: $digest" >&2
  exit 1
fi

cd "$(dirname "$0")/.."

# kustomize resolves `resources` relative to the kustomization and refuses an
# absolute path, so the overlay is created inside the repository.
mkdir -p hack/.release
work=$(mktemp -d hack/.release/manifest.XXXXXX)
trap 'rm -rf "$work"' EXIT

# A throwaway overlay on top of config/default: the only difference from the
# development bundle is the image reference.
if [ -n "$digest" ]; then
  image_field="digest: $digest"
else
  image_field="newTag: $version"
fi
cat >"$work/kustomization.yaml" <<EOF
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../../../config/default
images:
  - name: $repo
    $image_field
EOF

$KUSTOMIZE build "$work"
