# Changelog

This file is the source of the GitHub Release notes: the release workflow
takes the section of the version being released and appends the exact install
URL and image digest, which are only known once the image is published.

## v0.1.0

First public release.

ZoneRoute forwards DNS zones to upstream resolvers on a Kubernetes cluster
that uses CoreDNS. A `ZoneRoute` names the zones and an ordered list of
upstream IP addresses; the controller renders a CoreDNS configuration
fragment for every accepted route and publishes it into a single key of a
single ConfigMap, `kube-system/coredns-custom` → `zoneroute.server`. It never
edits the Corefile or the CoreDNS Deployment: CoreDNS picks the fragment up
through a one-time `import custom/*.server` wiring the administrator
establishes.

### Baseline

- Kubernetes 1.31 or newer — the CRD validates upstream addresses with the
  CEL IP library.
- CoreDNS 1.7.0 or newer with the `reload` plugin — from 1.7.0 the plugin
  detects changes in imported files.

### What it does

- `ZoneRoute` (`dns.mihnk.org/v1alpha1`, cluster-scoped): zones plus an
  ordered list of upstream resolvers, validated by the API server.
- Two status conditions, `Accepted` and `Published`, each with a reason and a
  message that names the zones, routes or files involved.
- Conflict resolution against other ZoneRoutes and against the zones CoreDNS
  already serves, whether they come from the Corefile or from another
  `*.server` key.
- Reserved-zone protection for the cluster domain, so a route can never
  capture service discovery.
- A deterministic fragment: the same routes always render the same bytes.
- Minimal RBAC — the controller may patch exactly one ConfigMap and may not
  create ConfigMaps at all.

### Validation

The kind and kubeadm CoreDNS layout is validated by a functional suite that
runs against real CoreDNS on every pull request. AKS and Gardener are
design-compatible but not validated by this project; EKS with the managed
add-on is unverified; GKE with kube-dns is not supported. See
[docs/coredns-wiring.md](docs/coredns-wiring.md).

### Known limitations

- CoreDNS only; there is no integration with any other DNS server.
- Kubernetes 1.31 and CoreDNS 1.7.0 are hard minimums.
- The standard CoreDNS listener on port 53 is assumed; a CoreDNS started with
  a non-default `-dns.port` is outside the integration contract.
- Upstreams are IP addresses only — hostnames are rejected.
- Upstreams are tried sequentially, in the order given; there is no other
  policy.
- Reverse zones are not managed: `in-addr.arpa` and `ip6.arpa` are rejected.
- The controller does not verify that CoreDNS reloaded the fragment, that the
  mount is healthy, or that an upstream resolver is reachable. `Published`
  reports what was written, not what resolves.
- The controller does not create `kube-system/coredns-custom` and does not
  wire CoreDNS; both remain installation steps.

### Documentation

[Installation](docs/install.md) ·
[CoreDNS wiring](docs/coredns-wiring.md) ·
[Troubleshooting](docs/troubleshooting.md)
