# CoreDNS wiring

ZoneRoute publishes a CoreDNS configuration fragment into one ConfigMap key.
For CoreDNS to load it, the cluster's CoreDNS has to import that key. This
is a one-time change that the controller never makes: on some platforms it
already exists, on others the administrator makes it. This guide describes
what must be true, then how to make it true on a kubeadm or kind cluster,
then what is known about managed platforms.

## The contract

1. The ConfigMap `kube-system/coredns-custom` exists. ZoneRoute writes the
   key `zoneroute.server` in it and nothing else.
2. The main Corefile (`kube-system/coredns`, key `Corefile`) contains a
   **top-level** line:

   ```
   import custom/*.server
   ```

   Top-level means outside any server block. The fragment contains complete
   server blocks (one per group of zones), so an import inside `.:53 { … }`
   is wrong and CoreDNS would refuse the configuration.
3. `coredns-custom` is mounted in the CoreDNS Pod at `/etc/coredns/custom`.
   The import path is relative to the Corefile's directory, `/etc/coredns`.
4. The mount is read-only.
5. The mount is a whole-ConfigMap volume, **not** a `subPath` mount. With
   `subPath` the kubelet never refreshes the files, so CoreDNS would keep
   serving the fragment it started with.
6. CoreDNS is ≥ 1.7.0 and the Corefile has the `reload` plugin. From 1.7.0
   `reload` hashes the parsed configuration, imports included, so a changed
   `zoneroute.server` is loaded within the reload interval (30 s ± 15 s by
   default) without restarting CoreDNS.
7. CoreDNS listens on port 53. ZoneRoute v0.1 identifies existing listeners
   as `zone:53`; a CoreDNS process started with a non-default `-dns.port` is
   outside the contract.

The controller checks point 2 (`Published` reason `CoreDNSNotWired` when
the import is missing) and point 1 (`IntegrationConfigMissing`). It cannot
see the mount, the plugin list or the reload result; those are confirmed by
asking DNS, see [Verifying the wiring](#verifying-the-wiring).

The glob tolerates an empty directory: `import custom/*.server` with no
matching file is a warning in the CoreDNS log, not an error, so the wiring
can be established before any route exists.

## kubeadm and kind

**Validated.** This is the stock kubeadm CoreDNS layout that kind ships; the
project's functional e2e suite (`make test-e2e`) runs the exact steps below
on Kubernetes 1.31 and stock CoreDNS before every scenario. The steps are
idempotent and preserve everything else in the Corefile and the Deployment.

### 1. The integration ConfigMap

```sh
kubectl -n kube-system get configmap coredns-custom \
  || kubectl -n kube-system create configmap coredns-custom
```

### 2. The top-level import

The kubeadm Corefile is one `.:53 { … }` block. Append the import after it:

```sh
kubectl -n kube-system get configmap coredns -o jsonpath='{.data.Corefile}' > Corefile
grep -q 'import custom/\*\.server' Corefile || printf '\nimport custom/*.server\n' >> Corefile
kubectl -n kube-system patch configmap coredns --type merge \
  -p "$(jq -n --rawfile c Corefile '{data: {Corefile: $c}}')"
```

The merge patch replaces only the `Corefile` key; other keys of the
ConfigMap, if any, are untouched. The result looks like:

```
.:53 {
    errors
    health {
       lameduck 5s
    }
    ready
    kubernetes cluster.local in-addr.arpa ip6.arpa {
       pods insecure
       fallthrough in-addr.arpa ip6.arpa
       ttl 30
    }
    prometheus :9153
    forward . /etc/resolv.conf {
       max_concurrent 1000
    }
    cache 30
    loop
    reload
    loadbalance
}
import custom/*.server
```

Confirm `reload` is in the block. If it is not, add it inside `.:53 { … }`.

### 3. The volume and mount

A strategic merge patch adds one volume and one mount; existing volumes and
mounts are kept because they merge by name and mount path. `optional: true`
keeps CoreDNS running if the ConfigMap is ever absent.

```sh
kubectl -n kube-system patch deployment coredns --type strategic -p '{
  "spec": {"template": {"spec": {
    "volumes": [{"name": "custom-config-volume", "configMap": {"name": "coredns-custom", "optional": true}}],
    "containers": [{"name": "coredns", "volumeMounts": [{"name": "custom-config-volume", "mountPath": "/etc/coredns/custom", "readOnly": true}]}]
  }}}}'
kubectl -n kube-system rollout status deployment/coredns
```

The Deployment change rolls the CoreDNS Pods, so they start with both the
new Corefile and the mount. Later changes to `coredns-custom` need no
rollout.

### The convenience script

`hack/wire-coredns.sh` runs the three steps above, in that order, skipping
each one that is already satisfied. It needs `kubectl` and `jq` and honours
`KUBECONFIG`. It is meant for clusters you administer yourself — kubeadm,
kind, and similar layouts where `kube-system/coredns` is a plain
Deployment. Do not run it against a managed platform: there the Corefile or
the Deployment may be owned by the provider and reverted, see below.

## Managed providers

The wording is deliberate. *Validated* means the repository contains a
repeatable check. *Design-compatible* means the platform's published
CoreDNS layout appears to satisfy the contract, but nothing in this
repository verifies it. *Unverified* and *unsupported* mean what they say.

### AKS — design-compatible, not validated

AKS ships `kube-system/coredns-custom` and a Corefile that already imports
`custom/*.server` at the top level, with the ConfigMap mounted at
`/etc/coredns/custom`. If your cluster matches that, steps 1–3 above are
already done and must not be repeated. `coredns-custom` may contain your
own `*.override` and `*.server` keys; ZoneRoute never writes them. There is
no AKS run in this repository's test suite.

### Gardener — design-compatible, not validated

Gardener-managed clusters expose a `coredns-custom` ConfigMap with a
`*.server` import in the same spirit. Check the actual Corefile and
Deployment of your cluster against the seven points of the contract before
creating routes. Not verified by this repository.

### EKS — unverified

With the managed CoreDNS add-on, the Corefile is owned by the add-on and set
through its `configurationValues`; a hand edit of `kube-system/coredns` can
be reverted, and the add-on also manages the CoreDNS Deployment, so the
volume and mount from step 3 may not survive an add-on update. Do not edit a
managed Corefile by hand expecting it to stick. Self-managed CoreDNS on EKS
is an ordinary Deployment and the kubeadm steps apply, but no EKS
configuration has been validated by this repository.

### GKE — unsupported in v0.1

GKE clusters use kube-dns or Cloud DNS, not CoreDNS. There is no integration
point for the fragment.

## Verifying the wiring

```sh
# the import line is present and top-level
kubectl -n kube-system get configmap coredns -o jsonpath='{.data.Corefile}' | grep -n 'import custom/\*\.server'

# the mount exists, read-only, at the expected path
kubectl -n kube-system get deployment coredns \
  -o jsonpath='{.spec.template.spec.containers[0].volumeMounts}'

# CoreDNS reloaded after the last change
kubectl -n kube-system logs -l k8s-app=kube-dns | grep -i reload
```

Then create a route and resolve a name in it from inside the cluster, as in
[install.md](install.md#resolve-from-inside-the-cluster). A route with
`Published=True` whose zone does not resolve after a couple of minutes
points at the mount or the reload, not at the controller.

## Unwiring

Only if you added the wiring exclusively for ZoneRoute and nothing else
uses `coredns-custom`; on managed platforms the wiring is the provider's
and stays. Reverse the steps:

```sh
kubectl -n kube-system get configmap coredns -o jsonpath='{.data.Corefile}' \
  | grep -v 'import custom/\*\.server' > Corefile
kubectl -n kube-system patch configmap coredns --type merge \
  -p "$(jq -n --rawfile c Corefile '{data: {Corefile: $c}}')"
kubectl -n kube-system patch deployment coredns --type json -p '[
  {"op": "remove", "path": "/spec/template/spec/containers/0/volumeMounts/1"},
  {"op": "remove", "path": "/spec/template/spec/volumes/1"}]'
```

Check the indexes before running the JSON patch: they refer to the position
of `custom-config-volume` in your Deployment, which is `1` when the kubeadm
layout has only its own `config-volume` at `0`. Do not delete
`coredns-custom` itself unless you created it and it holds nothing else.
