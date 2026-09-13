# zoneroute

Kubernetes-native conditional DNS forwarding for CoreDNS.

A `ZoneRoute` declares which DNS zones are forwarded to which upstream
resolvers. The controller publishes a CoreDNS configuration fragment for them
without touching the cluster's Corefile.

**Status:** pre-release. Phase 1 — the public API and project foundation — is
complete; the controller is not implemented yet.

## API

`dns.mihnk.org/v1alpha1`, cluster-scoped.

```yaml
apiVersion: dns.mihnk.org/v1alpha1
kind: ZoneRoute
metadata:
  name: corporate
spec:
  zones:
    - company.local
    - corp.internal
  upstreams:              # order is significant: first is tried first
    - address: 10.10.10.53
    - address: 10.10.10.54
      port: 5353          # optional, defaults to 53
```

Validation is enforced by the API server (schema and CEL):

- 1–64 zones; compared case-insensitively with the trailing dot ignored, so
  `Example.COM` and `example.com.` are the same zone and may not both appear
- `localhost`, `in-addr.arpa`, `ip6.arpa` and their subzones are reserved
- 1–8 upstreams; `address` must be a canonical IPv4 or IPv6 address; the
  same `(address, port)` may not appear twice

Status carries two conditions, `Accepted` and `Published`, plus
`observedGeneration`. `Published` means the controller wrote the fragment to
the integration point under the installation contract — not that CoreDNS
loaded it.

## Compatibility

| Component  | Minimum |
| ---------- | ------- |
| Kubernetes | 1.31 (CEL IP library) |
| Go         | 1.26    |

## Development

```sh
make generate         # deepcopy + CRD (controller-gen, pinned in the Makefile)
make verify-generate  # fail if generated files are stale
make vet
make test
make verify-crd       # install the CRD on a kind Kubernetes 1.31 cluster and
                      # exercise the validation rules; needs docker
```

Generated files carry a `Code generated … DO NOT EDIT.` header:
`api/v1alpha1/zz_generated.deepcopy.go` and
`config/crd/dns.mihnk.org_zoneroutes.yaml`. Edit `api/v1alpha1/types.go`
and run `make generate` instead.

## License

[Apache-2.0](LICENSE).
