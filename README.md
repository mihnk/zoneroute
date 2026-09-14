# zoneroute

Kubernetes-native conditional DNS forwarding for CoreDNS.

A `ZoneRoute` declares which DNS zones are forwarded to which upstream
resolvers. The controller renders a CoreDNS configuration fragment for all
accepted routes and publishes it into one key of one ConfigMap,
`kube-system/coredns-custom` → `zoneroute.server`. It never edits the
cluster's Corefile or the CoreDNS Deployment; CoreDNS picks the fragment up
through a one-time `import custom/*.server` wiring that the administrator
establishes once.

**Status:** v0.1.0 is released. Each release publishes an installation
manifest whose controller image is pinned by digest, and
`ghcr.io/mihnk/zoneroute` for `linux/amd64` and `linux/arm64` with an SBOM
and build provenance. The integration is exercised end to end on every pull
request against real CoreDNS on kind (Kubernetes 1.31, stock kubeadm layout).

The `install.yaml` committed to this repository is the **development**
manifest: it references `ghcr.io/mihnk/zoneroute:dev`, a naming convention
rather than a published image, and is meant for building the controller
yourself — see [Image](docs/install.md#from-a-checkout).

## Quick start

Full details, required privileges and the kustomize path:
[docs/install.md](docs/install.md).

```sh
# 1. CRD, namespace, RBAC and controller Deployment
kubectl apply -f https://github.com/mihnk/zoneroute/releases/latest/download/install.yaml

# 2. The integration ConfigMap. Never delete or replace an existing one.
kubectl -n kube-system get configmap coredns-custom \
  || kubectl -n kube-system create configmap coredns-custom

# 3. Wire CoreDNS once: top-level `import custom/*.server` and a read-only
#    mount of coredns-custom at /etc/coredns/custom. See docs/coredns-wiring.md;
#    on a kubeadm/kind cluster you administer yourself: hack/wire-coredns.sh

# 4. Wait for the controller
kubectl -n zoneroute-system rollout status deployment/zoneroute-controller

# 5. First route
kubectl apply -f - <<'EOF'
apiVersion: dns.mihnk.org/v1alpha1
kind: ZoneRoute
metadata:
  name: corporate
spec:
  zones:
    - company.local
  upstreams:
    - address: 10.10.10.53
    - address: 10.10.10.54
      port: 5353
EOF

# 6. Both conditions should be True
kubectl get zoneroutes

# 7. The published fragment
kubectl -n kube-system get configmap coredns-custom -o jsonpath='{.data.zoneroute\.server}'

# 8. Resolve from inside the cluster
kubectl run -it --rm dnsq --image=registry.k8s.io/e2e-test-images/agnhost:2.66.1 \
  --restart=Never -- dig +short host.company.local
```

## Documentation

- [Installation](docs/install.md) — prerequisites, install paths, first
  ZoneRoute, verification, ownership, uninstall.
- [CoreDNS wiring](docs/coredns-wiring.md) — the integration contract,
  kubeadm/kind steps, managed-provider notes.
- [Troubleshooting](docs/troubleshooting.md) — what each `Accepted` /
  `Published` reason means, and what to inspect when a name does not resolve.

## Compatibility

| Component | Minimum | Why |
| --- | --- | --- |
| Kubernetes | 1.31 | the CRD validates upstream addresses with the CEL IP library |
| CoreDNS | 1.7.0 | the `reload` plugin detects changes in imported files from 1.7.0 |
| Go | 1.26 | development only |

The integration is validated on kind/kubeadm-style CoreDNS by the e2e suite,
which runs against the supported minimum on every pull request and against one
newer pinned Kubernetes weekly. 1.31 remains the floor: the newer lane shows
forward compatibility, it does not widen what is supported. Other environments
are described conservatively in
[docs/coredns-wiring.md](docs/coredns-wiring.md#managed-providers).

## Development

```sh
make generate         # deepcopy, CRD and install.yaml (controller-gen and kustomize, pinned in the Makefile)
make verify-generate  # fail if generated files are stale
make vet
make test             # unit and static manifest tests
make build-image      # local controller image (IMAGE=name:tag to override)
make verify-crd       # CRD schema and CEL rules on a kind Kubernetes 1.31 cluster; needs docker
make verify-install   # install.yaml applies cleanly on a kind Kubernetes 1.31 cluster; needs docker
make test-e2e         # functional suite against real CoreDNS on kind; needs docker

VERSION=v0.1.0 make release-check   # render the release artifacts locally; publishes nothing
```

Cutting a release: [docs/release.md](docs/release.md).

Generated files carry a `Code generated … DO NOT EDIT.` header:
`api/v1alpha1/zz_generated.deepcopy.go` and
`config/crd/dns.mihnk.org_zoneroutes.yaml`; `install.yaml` is rendered from
`config/default`. Edit `api/v1alpha1/types.go` or `config/` and run
`make generate` instead.

## License

[Apache-2.0](LICENSE).
