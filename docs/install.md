# Installing ZoneRoute

This guide takes a cluster with CoreDNS to a working first `ZoneRoute`. It
covers the controller; the one-time CoreDNS wiring has its own guide,
[coredns-wiring.md](coredns-wiring.md).

Sequence:

1. [Prerequisites](#prerequisites)
2. [Install the CRD and controller](#install)
3. [Ensure `coredns-custom` exists](#the-coredns-custom-configmap)
4. [Wire CoreDNS](#wire-coredns)
5. [Wait for the controller](#wait-for-the-controller)
6. [Create a ZoneRoute](#your-first-zoneroute)
7. [Check `Accepted` and `Published`](#verify)
8. [Resolve a name](#resolve-from-inside-the-cluster)

## Prerequisites

| Requirement | Detail |
| --- | --- |
| Kubernetes ≥ 1.31 | The CRD validates upstream addresses with the CEL IP library (`isIP`, `ip.isCanonical`), available from 1.31. Older API servers reject the CRD. |
| CoreDNS ≥ 1.7.0 with the `reload` plugin | From 1.7.0 `reload` hashes the parsed Corefile, imports included, so a change to `coredns-custom` is picked up without restarting CoreDNS. The kubeadm default Corefile enables `reload`. |
| CoreDNS configured from `kube-system/coredns` | The controller reads the `Corefile` key of that ConfigMap to detect wiring and existing zones. If the ConfigMap or the key is missing it publishes nothing, deliberately. |
| CoreDNS listening on port 53 | ZoneRoute assumes the default DNS listener. A CoreDNS started with a non-default `-dns.port` is outside the integration contract. |
| `kubectl` | Any version compatible with your cluster. |

Installation privileges — a cluster-admin satisfies all of them, but the
actual requirements are:

- create the cluster-scoped `ZoneRoute` CustomResourceDefinition;
- create a ClusterRole and ClusterRoleBinding;
- create a Role and RoleBinding in `kube-system`;
- create the `zoneroute-system` namespace and its objects;
- edit the CoreDNS ConfigMap and Deployment in `kube-system`, only when you
  establish the wiring yourself.

## What gets installed

`install.yaml` is rendered from `config/default` and contains exactly ten
objects:

| Object | Name | Namespace |
| --- | --- | --- |
| Namespace (Pod Security `restricted`) | `zoneroute-system` | — |
| CustomResourceDefinition | `zoneroutes.dns.mihnk.org` | — |
| ServiceAccount | `zoneroute-controller` | `zoneroute-system` |
| ClusterRole + ClusterRoleBinding | `zoneroute-controller` | — |
| Role + RoleBinding (CoreDNS ConfigMaps) | `zoneroute-coredns` | `kube-system` |
| Role + RoleBinding (leader election Lease) | `zoneroute-leader-election` | `zoneroute-system` |
| Deployment (1 replica) | `zoneroute-controller` | `zoneroute-system` |

The controller runs as a non-root user (uid 65532) with a read-only root
filesystem, no capabilities and no host access. Its permissions are the
minimum for what it does:

| Resource | Verbs | Why |
| --- | --- | --- |
| `zoneroutes` (cluster) | list, watch | read routes |
| `zoneroutes/status` | update | write `Accepted` / `Published` |
| `configmaps` in `kube-system` | list, watch | read `coredns` and `coredns-custom` |
| `configmaps/coredns-custom` in `kube-system` | patch | publish the fragment |
| `leases` in `zoneroute-system` | create; get, update on its own Lease | leader election |

Not granted: get, create, update or delete on any ConfigMap; anything on
`coredns`; Secrets, Pods, Services, Deployments, Events.

## Install

### From a release (recommended)

Each release publishes an installation manifest whose controller image is
pinned by digest, so the manifest names the exact image bytes rather than a
tag someone could move:

```sh
# the current release
kubectl apply -f https://github.com/mihnk/zoneroute/releases/latest/download/install.yaml

# or an exact version
kubectl apply -f https://github.com/mihnk/zoneroute/releases/download/v0.2.0/install.yaml
```

This installs everything in [What gets installed](#what-gets-installed). It
does **not** create `coredns-custom` and does not touch CoreDNS; those are
the next two steps.

The images are published to `ghcr.io/mihnk/zoneroute` for `linux/amd64` and
`linux/arm64`. Each release names its digest, and carries an SBOM and build
provenance; `gh attestation verify oci://ghcr.io/mihnk/zoneroute@sha256:… --repo mihnk/zoneroute`
checks that the image came from this repository's release workflow.

### From a checkout

The `install.yaml` committed to the repository is the **development**
manifest: it references `ghcr.io/mihnk/zoneroute:dev`, which is a naming
convention and not a published image. Use it when you build the controller
yourself:

```sh
make build-image IMAGE=registry.example.com/zoneroute:dev
docker push registry.example.com/zoneroute:dev   # or: kind load docker-image …
```

and point the manifest at your image with the kustomize overlay below
(`images:`).

### With kustomize

`config/default` is the same bundle as a kustomize base. Apply it directly:

```sh
kubectl apply -k config/default
```

or write an overlay to change the image or the cluster domain:

```yaml
# kustomization.yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - github.com/mihnk/zoneroute/config/default?ref=main
images:
  - name: ghcr.io/mihnk/zoneroute
    newName: registry.example.com/zoneroute
    newTag: dev
patches:
  - target:
      kind: Deployment
      name: zoneroute-controller
    patch: |-
      apiVersion: apps/v1
      kind: Deployment
      metadata:
        name: zoneroute-controller
        namespace: zoneroute-system
      spec:
        template:
          spec:
            containers:
              - name: controller
                args:
                  - --cluster-domain=corp.example
                  - --leader-elect=true
                  - --health-probe-bind-address=:8081
                  - --metrics-bind-address=0
```

```sh
kubectl apply -k .
```

### Cluster domain

The controller refuses zones inside the Kubernetes cluster domain and its
descendants (`svc.cluster.local`, for example) so a route can never capture
service DNS. It does **not** detect the domain; it takes it from the flag:

```
--cluster-domain=cluster.local
```

The default is `cluster.local`. If your cluster uses a different domain, set
the flag in the Deployment (the overlay above shows how). The full set of
flags:

| Flag | Default | Meaning |
| --- | --- | --- |
| `--cluster-domain` | `cluster.local` | Kubernetes cluster domain; ZoneRoutes inside it are rejected as reserved |
| `--leader-elect` | `true` | only one instance reconciles |
| `--health-probe-bind-address` | `:8081` | `/healthz` and `/readyz` |
| `--metrics-bind-address` | `0` | metrics endpoint; `0` disables it |

## The coredns-custom ConfigMap

The controller publishes into `kube-system/coredns-custom` and **never
creates it**: creating it is an installation decision, because on some
platforms it already exists and carries provider- or user-managed keys.

Check first:

```sh
kubectl -n kube-system get configmap coredns-custom
```

Only if it does not exist:

```sh
kubectl -n kube-system create configmap coredns-custom
```

Never delete or replace an existing `coredns-custom`, and never apply a
manifest that redefines it: `kubectl apply`/`replace` would drop keys you do
not own. Ownership inside the ConfigMap is by key:

| Key | Owner |
| --- | --- |
| `zoneroute.server` | ZoneRoute. Rewritten on every reconcile; hand edits are overwritten. |
| every other key | administrator or provider. ZoneRoute reads `*.server` keys to detect zones CoreDNS already serves and never writes them. |

While the ConfigMap is missing, accepted routes report
`Published=False` with reason `IntegrationConfigMissing` and the message
`ConfigMap kube-system/coredns-custom does not exist; create it to enable publishing.`
Creating it is enough; the controller notices and publishes.

## Wire CoreDNS

CoreDNS has to import the fragment. That is a one-time change to the
CoreDNS Corefile and Deployment, made by the administrator or already
provided by the platform — never by the controller. Follow
[coredns-wiring.md](coredns-wiring.md). On a kubeadm or kind cluster you
administer yourself, `hack/wire-coredns.sh` performs exactly the documented
steps.

Until the wiring is in place, accepted routes report `Published=False` with
reason `CoreDNSNotWired`; the fragment is still written to `coredns-custom`
so the moment the import appears it takes effect.

## Wait for the controller

```sh
kubectl -n zoneroute-system rollout status deployment/zoneroute-controller
kubectl -n zoneroute-system logs deployment/zoneroute-controller
```

The log shows the Lease being acquired and the two event sources
(`ZoneRoute`, `ConfigMap`) starting. One line saying Events are forbidden
is expected: the controller is not granted Event permissions and leader
election's event recording is best-effort.

## Your first ZoneRoute

```yaml
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
```

```sh
kubectl apply -f corporate.yaml
```

What the fields mean:

- **`zones` are DNS zones, not records.** `company.local` routes every name
  under it. Names are compared case-insensitively and with the trailing dot
  ignored.
- **`upstreams` are IP addresses only**, canonical IPv4 or IPv6. Hostnames
  are rejected by the API server.
- **Order is significant.** The first upstream is tried first; the next one
  is used when it fails. This is CoreDNS `forward` with `policy sequential`,
  the only strategy ZoneRoute generates.
- **`port` defaults to 53.**
- **Parent and child zones may coexist**, in one route or across routes:
  `company.local` and `eu.company.local` can point to different upstreams and
  CoreDNS picks the longest match.
- **Two routes may not claim the same zone.** The older route wins; the newer
  one reports `Accepted=False` / `ZoneConflict`.
- **Reverse zones are outside the contract.** `in-addr.arpa`, `ip6.arpa` and their
  subzones are rejected by the API server, as is `localhost`.
- **The cluster domain and its descendants are reserved** and rejected with
  `ReservedZone`; see [Cluster domain](#cluster-domain).

The full validation rules live in the CRD (`config/crd`); `kubectl explain
zoneroute.spec` prints them.

## Verify

```sh
kubectl get zoneroutes
```

```
NAME        ACCEPTED   PUBLISHED   AGE
corporate   True       True        12s
```

```sh
kubectl get zoneroute corporate -o yaml
```

```yaml
status:
  observedGeneration: 1
  conditions:
    - type: Accepted
      status: "True"
      reason: Accepted
      message: All zones accepted.
    - type: Published
      status: "True"
      reason: Published
      message: Fragment published to kube-system/coredns-custom (zoneroute.server).
```

The two conditions, at installation level:

- **`Accepted=True`** — the route is logically accepted by ZoneRoute: valid,
  no zone reserved, no conflict with another route or with a zone CoreDNS
  already serves.
- **`Published=True`** — ZoneRoute wrote its fragment into
  `coredns-custom` and found the `import custom/*.server` wiring in the
  Corefile.

`Published=True` does **not** prove that:

- CoreDNS reloaded the file;
- the upstream resolvers are reachable;
- the DNS answer is correct;
- the `coredns-custom` mount in the CoreDNS Pod is healthy.

Those are verified below, by asking DNS. Every `Accepted` / `Published`
reason, what causes it and what to inspect, is in the
[reason reference](troubleshooting.md#reason-reference).

### The published fragment

```sh
kubectl -n kube-system get configmap coredns-custom -o jsonpath='{.data.zoneroute\.server}'
```

```
# Managed by ZoneRoute. Edits are overwritten.

# zoneroute: corporate (generation 1)
company.local {
    forward . 10.10.10.53 10.10.10.54:5353 {
        policy sequential
    }
}
```

The fragment is deterministic: same routes, same bytes. Only accepted routes
appear in it.

### Resolve from inside the cluster

Your workstation does not use cluster DNS, so query from a Pod. This is the
image and tool the project's e2e suite uses:

```sh
kubectl run -it --rm dnsq --image=registry.k8s.io/e2e-test-images/agnhost:2.66.1 \
  --restart=Never -- dig +short host.company.local
```

Allow for propagation the first time: the kubelet refreshes ConfigMap
mounts on its sync period (up to about a minute) and CoreDNS `reload` checks
every 30 s with 15 s of jitter. CoreDNS logs the reload:

```sh
kubectl -n kube-system logs -l k8s-app=kube-dns | grep -i reload
```

Ordinary cluster DNS is unaffected; `kubernetes.default.svc.cluster.local`
keeps resolving to the API server's ClusterIP.

## Ownership and safety

ZoneRoute:

- reads `kube-system/coredns` (`Corefile`);
- reads and watches `kube-system/coredns-custom`;
- patches only `coredns-custom` → `data["zoneroute.server"]`, with a JSON
  merge patch that cannot touch other keys — and its RBAC allows `patch` on
  that one ConfigMap and nothing else;
- does not create `coredns-custom`;
- does not modify the Corefile;
- does not modify the CoreDNS Deployment;
- does not validate that upstream resolvers are reachable.

The administrator or provider owns:

- the CoreDNS Deployment;
- the Corefile wiring (`import custom/*.server`, the mount);
- the lifecycle of `coredns-custom`;
- every key in `coredns-custom` other than `zoneroute.server`;
- network reachability from CoreDNS Pods to the upstream resolvers.

## Uninstall

Order matters: the controller has to see the routes disappear before it is
removed, otherwise their blocks stay in the fragment.

```sh
# 1. Remove the routes; the controller rewrites the fragment without them.
kubectl delete zoneroutes --all
kubectl -n kube-system get configmap coredns-custom -o jsonpath='{.data.zoneroute\.server}'
#    -> only the "# Managed by ZoneRoute" header remains

# 2. Remove the controller, RBAC, namespace and CRD.
kubectl delete -f install.yaml

# 3. Optional: drop ZoneRoute's key. Do NOT delete the ConfigMap.
kubectl -n kube-system patch configmap coredns-custom --type merge -p '{"data":{"zoneroute.server":null}}'
```

Optional last step: remove the `import custom/*.server` line and the
`/etc/coredns/custom` mount — **only** if you added them exclusively for
ZoneRoute and nothing else uses `coredns-custom`. On platforms where the
wiring is provider-managed, leave it alone. See
[Unwiring](coredns-wiring.md#unwiring).
