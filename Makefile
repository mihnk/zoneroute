# Generators and verification tools are pinned here rather than in go.mod so
# the runtime module graph stays limited to what the binary needs.
CONTROLLER_GEN := go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.21.0
KUSTOMIZE      := go run sigs.k8s.io/kustomize/kustomize/v5@v5.8.1

# Two Kubernetes lanes, each pinned by tag and digest. A node image is only
# published by one kind release, so the kind version travels with it.
#
#   minimum — the supported floor, and the authoritative target. Every
#             pull request, the release workflow and the local `make`
#             targets use it.
#   newer   — forward-compatibility only, run on a weekly cadence by
#             .github/workflows/compatibility.yml. It does not move the
#             floor, and a failure there is a compatibility report, not a
#             change to what ZoneRoute supports.
#
# Bumping a lane means changing the two values that belong to it, here and
# nowhere else.
KIND_MINIMUM            := go run sigs.k8s.io/kind@v0.31.0
KIND_NODE_IMAGE_MINIMUM := kindest/node:v1.31.14@sha256:6f86cf509dbb42767b6e79debc3f2c32e4ee01386f0489b3b2be24b0a55aac2b

KIND_NEWER            := go run sigs.k8s.io/kind@v0.33.0
KIND_NODE_IMAGE_NEWER := kindest/node:v1.34.11@sha256:44e222ee2132dab25ff87301682f89eb82c7880ea3a1bf543bfe9708fd08d67d

# The lane the cluster targets use. Override both together to test another
# Kubernetes version:
#
#   make test-e2e KIND="$(KIND_NEWER)" KIND_NODE_IMAGE="$(KIND_NODE_IMAGE_NEWER)"
#
# or, equivalently, `make test-e2e-newer`.
KIND            ?= $(KIND_MINIMUM)
KIND_NODE_IMAGE ?= $(KIND_NODE_IMAGE_MINIMUM)

# Local controller image for the e2e suite; test/e2e/overlay pins the same
# name, so it is not overridable here.
E2E_IMAGE := zoneroute-controller:e2e

.PHONY: generate install-manifest verify-generate build build-image vet test verify-crd verify-crd-newer verify-install verify-install-newer test-e2e test-e2e-newer release-check release-manifest

## generate: regenerate deepcopy code, the CRD manifest and install.yaml.
generate:
	$(CONTROLLER_GEN) object paths=./api/...
	$(CONTROLLER_GEN) crd paths=./api/... output:crd:dir=config/crd
	$(MAKE) install-manifest

## install-manifest: render install.yaml from config/default.
install-manifest:
	$(KUSTOMIZE) build config/default > install.yaml

## verify-generate: fail if generated files are out of date. Only generated
## paths are compared: config/ also holds hand-written manifests and tests,
## which config/install_test.go covers instead.
verify-generate: generate
	git diff --exit-code -- api config/crd install.yaml

## build: compile the controller binary.
build:
	go build -o bin/zoneroute-controller ./cmd/zoneroute-controller

## build-image: build the controller image locally (IMAGE=name:tag).
IMAGE ?= $(E2E_IMAGE)
build-image:
	docker build -t $(IMAGE) .

vet:
	go vet ./...

## test: unit tests. The e2e suite is behind the e2e build tag and never
## runs here.
test:
	go test -race ./...

## verify-crd: install the CRD on a throwaway cluster of the selected lane
## (the minimum by default) and check that the API server enforces the schema
## and CEL rules.
verify-crd:
	KIND="$(KIND)" KIND_NODE_IMAGE="$(KIND_NODE_IMAGE)" hack/verify-crd.sh

## verify-install: apply install.yaml to a throwaway cluster of the selected
## lane (the minimum by default) and check that the API server accepts every
## object.
verify-install:
	KIND="$(KIND)" KIND_NODE_IMAGE="$(KIND_NODE_IMAGE)" hack/verify-install.sh

## test-e2e: functional suite on a throwaway kind cluster of the selected
## lane (the minimum by default) with real CoreDNS. KEEP_E2E_CLUSTER=1 keeps
## the cluster on exit.
test-e2e:
	KIND="$(KIND)" KIND_NODE_IMAGE="$(KIND_NODE_IMAGE)" KUSTOMIZE="$(KUSTOMIZE)" E2E_IMAGE="$(E2E_IMAGE)" hack/e2e.sh

# The newer lane: the same three targets, the same scripts, one override.
# Forward-compatibility only; the minimum lane remains authoritative.
NEWER = KIND="$(KIND_NEWER)" KIND_NODE_IMAGE="$(KIND_NODE_IMAGE_NEWER)"

## verify-crd-newer: verify-crd against the newer pinned Kubernetes.
verify-crd-newer:
	$(MAKE) verify-crd $(NEWER)

## verify-install-newer: verify-install against the newer pinned Kubernetes.
verify-install-newer:
	$(MAKE) verify-install $(NEWER)

## test-e2e-newer: the functional suite against the newer pinned Kubernetes.
test-e2e-newer:
	$(MAKE) test-e2e $(NEWER)

## release-check: validate VERSION and render the release artifacts locally.
## Publishes nothing. Example: VERSION=v0.1.0 make release-check
release-check:
	KUSTOMIZE="$(KUSTOMIZE)" hack/release-check.sh

## release-manifest: print the release install manifest. Pass DIGEST for a
## real release, or VERSION for a dry run. Silent: the output is a manifest,
## so `make release-manifest > install.yaml` must not carry a recipe line.
release-manifest:
	@KUSTOMIZE="$(KUSTOMIZE)" hack/release-manifest.sh
