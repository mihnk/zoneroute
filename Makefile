# Generators and verification tools are pinned here rather than in go.mod so
# the runtime module graph stays limited to what the binary needs.
CONTROLLER_GEN := go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.21.0
KUSTOMIZE      := go run sigs.k8s.io/kustomize/kustomize/v5@v5.8.1
KIND           := go run sigs.k8s.io/kind@v0.31.0

# Kubernetes 1.31 is the supported cluster floor. The image is pinned by
# digest; it was published with kind v0.31.0, so KIND must stay on that
# release family.
KIND_NODE_IMAGE := kindest/node:v1.31.14@sha256:6f86cf509dbb42767b6e79debc3f2c32e4ee01386f0489b3b2be24b0a55aac2b

# Local controller image for the e2e suite; test/e2e/overlay pins the same
# name, so it is not overridable here.
E2E_IMAGE := zoneroute-controller:e2e

.PHONY: generate install-manifest verify-generate build build-image vet test verify-crd verify-install test-e2e release-check release-manifest

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

## verify-crd: install the CRD on a throwaway Kubernetes 1.31 cluster and
## check that the API server enforces the schema and CEL rules.
verify-crd:
	KIND="$(KIND)" KIND_NODE_IMAGE="$(KIND_NODE_IMAGE)" hack/verify-crd.sh

## verify-install: apply install.yaml to a throwaway Kubernetes 1.31 cluster
## and check that the API server accepts every object.
verify-install:
	KIND="$(KIND)" KIND_NODE_IMAGE="$(KIND_NODE_IMAGE)" hack/verify-install.sh

## test-e2e: functional suite on a throwaway kind 1.31 cluster with real
## CoreDNS. KEEP_E2E_CLUSTER=1 keeps the cluster on exit.
test-e2e:
	KIND="$(KIND)" KIND_NODE_IMAGE="$(KIND_NODE_IMAGE)" KUSTOMIZE="$(KUSTOMIZE)" E2E_IMAGE="$(E2E_IMAGE)" hack/e2e.sh

## release-check: validate VERSION and render the release artifacts locally.
## Publishes nothing. Example: VERSION=v0.1.0 make release-check
release-check:
	KUSTOMIZE="$(KUSTOMIZE)" hack/release-check.sh

## release-manifest: print the release install manifest. Pass DIGEST for a
## real release, or VERSION for a dry run. Silent: the output is a manifest,
## so `make release-manifest > install.yaml` must not carry a recipe line.
release-manifest:
	@KUSTOMIZE="$(KUSTOMIZE)" hack/release-manifest.sh
