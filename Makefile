# Generators and verification tools are pinned here rather than in go.mod so
# the runtime module graph stays limited to what the binary needs.
CONTROLLER_GEN := go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.21.0
KIND           := go run sigs.k8s.io/kind@v0.31.0

# Kubernetes 1.31 is the supported cluster floor. The image is pinned by
# digest; it was published with kind v0.31.0, so KIND must stay on that
# release family.
KIND_NODE_IMAGE := kindest/node:v1.31.14@sha256:6f86cf509dbb42767b6e79debc3f2c32e4ee01386f0489b3b2be24b0a55aac2b

.PHONY: generate verify-generate vet test verify-crd

## generate: regenerate deepcopy code and the CRD manifest.
generate:
	$(CONTROLLER_GEN) object paths=./api/...
	$(CONTROLLER_GEN) crd paths=./api/... output:crd:dir=config/crd

## verify-generate: fail if generated files are out of date.
verify-generate: generate
	git diff --exit-code -- api config

vet:
	go vet ./...

test:
	go test -race ./...

## verify-crd: install the CRD on a throwaway Kubernetes 1.31 cluster and
## check that the API server enforces the schema and CEL rules.
verify-crd:
	KIND="$(KIND)" KIND_NODE_IMAGE="$(KIND_NODE_IMAGE)" hack/verify-crd.sh
