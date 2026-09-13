#!/usr/bin/env bash
# Validates a release version and renders the release artifacts locally,
# without building, pushing or publishing anything.
#
#   VERSION=vX.Y.Z make release-check
#
# With no VERSION, only the self-checks of the version parser run.
set -euo pipefail

cd "$(dirname "$0")/.."

# Same pinned kustomize as the Makefile; override to use another build.
KUSTOMIZE="${KUSTOMIZE:-go run sigs.k8s.io/kustomize/kustomize/v5@v5.8.1}"

# semver VERSION -> 0 when VERSION is a tag this project releases:
# "v" plus SemVer, with an optional prerelease. Build metadata (+...) is
# rejected: it cannot appear in a container tag.
semver() {
  [[ "$1" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$ ]]
}

# prerelease VERSION -> 0 when the version carries a prerelease suffix.
prerelease() {
  [[ "$1" == *-* ]]
}

self_check() {
  local fail=0 v
  for v in v0.1.0 v1.2.3 v10.20.30 v0.1.0-rc.1 v1.0.0-alpha v0.0.1; do
    semver "$v" || { echo "  FAIL  $v should be accepted"; fail=1; }
  done
  for v in 0.1.0 v1 v1.2 v1.2.3.4 v01.2.3 v1.2.3+build vX.Y.Z "" "v1.2.3 "; do
    semver "$v" && { echo "  FAIL  $v should be rejected"; fail=1; }
  done
  for v in v0.1.0-rc.1 v1.0.0-alpha; do
    prerelease "$v" || { echo "  FAIL  $v is a prerelease"; fail=1; }
  done
  for v in v0.1.0 v1.2.3; do
    prerelease "$v" && { echo "  FAIL  $v is not a prerelease"; fail=1; }
  done
  [ "$fail" -eq 0 ] || exit 1
  echo "  ok    version parser"
}

echo "==> version parser self-check"
self_check

version="${VERSION:-}"
if [ -z "$version" ]; then
  echo "==> no VERSION given; skipping artifact rendering"
  echo "    run: VERSION=v0.1.0 make release-check"
  exit 0
fi

echo "==> validating $version"
if ! semver "$version"; then
  echo "  FAIL  not a release version: $version" >&2
  exit 1
fi
if prerelease "$version"; then
  echo "  ok    $version (prerelease: GitHub Release marked prerelease, latest not moved)"
else
  echo "  ok    $version (stable)"
fi

out="${OUT_DIR:-hack/.release/$version}"
rm -rf "$out"
mkdir -p "$out"

echo "==> rendering the release manifest (no digest: dry run pins the tag)"
VERSION="$version" DIGEST="" hack/release-manifest.sh >"$out/install.yaml"
grep -E '^\s+image:' "$out/install.yaml" | sed 's/^/  /'

echo "==> checksums"
(cd "$out" && shasum -a 256 install.yaml >checksums.txt)
sed 's/^/  /' "$out/checksums.txt"

echo "==> release notes"
VERSION="$version" DIGEST="" hack/release-notes.sh >"$out/notes.md"
head -n 5 "$out/notes.md" | sed 's/^/  /'

echo "==> wrote $out"
echo "    a real release pins the image by digest; this dry run pins the tag."
