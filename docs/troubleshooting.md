# Troubleshooting

What each ZoneRoute condition means, and how to get from "the name does not
resolve" to the component that is actually responsible.

Installation and wiring are covered elsewhere: [install.md](install.md),
[coredns-wiring.md](coredns-wiring.md).

## Start here

Collect evidence before changing anything. None of these commands modify the
cluster:

```sh
# 1. the routes and their two conditions
kubectl get zoneroutes

# 2. the full status of the route in question
kubectl get zoneroute <name> -o yaml

# 3. what the controller published
kubectl -n kube-system get configmap coredns-custom -o jsonpath='{.data.zoneroute\.server}'

# 4. what CoreDNS is configured with
kubectl -n kube-system get configmap coredns -o jsonpath='{.data.Corefile}'

# 5. what the controller has been doing
kubectl -n zoneroute-system logs deployment/zoneroute-controller --tail=100
```

Do not start by editing the Corefile, and do not restart CoreDNS or the
controller before you have read the status and the logs. A restart destroys
the evidence and rarely fixes a configuration problem.

## Reading ZoneRoute status

A ZoneRoute carries exactly two conditions. There is no `Ready` condition,
and no condition reports whether DNS actually works.

```sh
kubectl get zoneroutes
```

```
NAME        ACCEPTED   PUBLISHED   AGE
corporate   True       True        4m
```

`status.observedGeneration` says which `metadata.generation` the conditions
describe. If it is behind, the conditions predate your last edit — see
[Status appears stale](#status-appears-stale).

### Accepted

Accepted answers: **is this ZoneRoute logically safe for ZoneRoute to
include?** It is decided from the route itself, the other ZoneRoutes, and the
CoreDNS configuration the controller can see.

Order of evaluation:

```
zone inside the cluster domain          -> ReservedZone
zone already served by CoreDNS          -> ZoneOwnedByCoreDNS
zone claimed by an older ZoneRoute      -> ZoneConflict
otherwise                               -> accepted
```

and only then, for an accepted route:

```
unresolved imports were seen            -> AcceptedWithUnresolvedImports
otherwise                               -> Accepted
```

`AcceptedWithUnresolvedImports` is an accepted state with reduced inspection
visibility, not another kind of conflict.

### Published

Published answers: **was this accepted route published into the integration
point under the documented installation contract?** It describes what the
controller did with the ConfigMap and what it saw in the Corefile.

### What Published=True does not prove

`Published=True` means the fragment was written to
`kube-system/coredns-custom` and the `import custom/*.server` line was found
in the Corefile. It does **not** prove that:

- CoreDNS has reloaded the generated fragment;
- the `coredns-custom` mount in the CoreDNS Pod is healthy;
- the kubelet has propagated the ConfigMap update yet;
- the upstream DNS server is reachable;
- the upstream DNS server returns the expected answer;
- network policy or a firewall allows UDP or TCP port 53;
- an application Pod uses the expected cluster DNS server;
- DNS resolution actually succeeds.

Everything in that list is verified by querying DNS, not by reading status.
See [DNS does not resolve](#dns-does-not-resolve).

## Reason reference

### Accepted reasons

| Reason | Status | Meaning |
| --- | --- | --- |
| [`Accepted`](#accepted-reason-accepted) | True | passed reservation and conflict checks; eligible for rendering |
| [`AcceptedWithUnresolvedImports`](#acceptedwithunresolvedimports) | True | accepted, but some CoreDNS imports could not be inspected |
| [`ReservedZone`](#reservedzone) | False | a zone lies inside the configured cluster domain |
| [`ZoneOwnedByCoreDNS`](#zoneownedbycoredns) | False | CoreDNS already serves that listener |
| [`ZoneConflict`](#zoneconflict) | False | an older ZoneRoute already claims the zone |

### Published reasons

| Reason | Status | Meaning |
| --- | --- | --- |
| [`Published`](#published-reason-published) | True | fragment written and the Corefile import was detected |
| [`NotAccepted`](#notaccepted) | False | the route is not accepted; nothing was published for it |
| [`IntegrationConfigMissing`](#integrationconfigmissing) | False | `kube-system/coredns-custom` does not exist |
| [`CoreDNSNotWired`](#corednsnotwired) | False | fragment written, but the Corefile has no top-level import |
| [`FragmentInvalid`](#fragmentinvalid) | False | the controller's own output failed validation; internal error |
| [`WriteFailed`](#writefailed) | False | the ConfigMap patch was rejected by the API server |

Every reason above is a constant in `api/v1alpha1/types.go`; each condition
carries a `message` naming the specific zones, routes or files involved.

> Condition messages are intended for humans and may change between releases.
> Automation should use condition `type`, `status`, and `reason` instead of
> parsing message text.

### Accepted: reason `Accepted`

**Means.** The route passed the reserved-zone and conflict checks and is
rendered into the fragment.

**Says nothing about** publication or DNS. A route can be `Accepted=True`
and `Published=False`, and it can be both True while no Pod resolves the
name. Read the `Published` condition next, then query DNS.

### AcceptedWithUnresolvedImports

**Means.** The route is accepted (`Accepted=True`), but while inspecting the
CoreDNS configuration the controller found top-level `import` statements it
cannot follow: they point at files on the CoreDNS Pod's filesystem, which
the controller never reads. Conflict detection is therefore incomplete for
whatever those files define. This is an observability limitation, not
automatically a configuration error.

ZoneRoute inspects the `Corefile` key of `kube-system/coredns` and the
`*.server` keys of `kube-system/coredns-custom`. It cannot inspect arbitrary
filesystem imports, and it does not try.

**Inspect.** The condition message lists every unresolved import as
`source:line pattern`:

```sh
kubectl get zoneroute <name> -o yaml
```

```yaml
    - type: Accepted
      status: "True"
      reason: AcceptedWithUnresolvedImports
      message: 'Accepted. Conflict detection could not inspect these imports:
        Corefile:14 /etc/coredns/extra/*.conf.'
```

Then read those files yourself, from the CoreDNS Pod's point of view, and
check whether any of them opens a server block for a zone your route claims:

```sh
kubectl -n kube-system get configmap coredns -o jsonpath='{.data.Corefile}' | grep -n import
```

**Remediation.** If the imported files define no overlapping zone, nothing
needs to change; the reason is informational. If they do, the effective
behaviour is decided by CoreDNS, not by ZoneRoute: remove the overlap from
one side. Note that block-level imports (inside a server block) and snippet
imports are resolved by the controller and never produce this reason.

### ReservedZone

**Means.** One or more zones lie inside the Kubernetes cluster domain or are
the cluster domain itself — `cluster.local`, `svc.cluster.local`,
`example.svc.cluster.local`. ZoneRoute refuses them so a route can never
capture service discovery. The message names the zones and the domain:

```
Zones inside the cluster domain cluster.local. are reserved: svc.cluster.local.
```

**Cause.** Either the zone really is inside the cluster domain, or the
controller is running with the wrong `--cluster-domain`. The flag is
authoritative: the controller does not detect the cluster domain.

**Inspect.**

```sh
kubectl -n zoneroute-system get deployment zoneroute-controller \
  -o jsonpath='{.spec.template.spec.containers[0].args}'
```

**Remediation.** Use a zone outside the cluster domain. If your cluster genuinely
uses a different domain than the flag says, correct the flag — see
[Cluster domain](install.md#cluster-domain). `localhost`, `in-addr.arpa` and
`ip6.arpa` are rejected earlier, by the API server's own validation, and
never reach this condition.

**Do not** set `--cluster-domain` to something narrower in order to slip a
route past the check. The protection exists because a forwarded cluster zone
breaks service discovery for the whole cluster.

### ZoneOwnedByCoreDNS

**Means.** The visible CoreDNS configuration already serves the same
listener. A listener's identity is **transport + canonical zone +
port**, and a ZoneRoute always generates `("dns", <canonical zone>, 53)`.
So an existing `dns` listener for `example.com` on port 53 conflicts; the
same zone on a different port, or under a different transport such as `tls`
or `grpc`, does not. Parent and child zones are independent identities:
`example.com` and `eu.example.com` never conflict with each other.

The message names the source, which is either the main Corefile or a
`*.server` key of `coredns-custom`:

```
Zone example.com. is already served by CoreDNS (Corefile)
Zone example.com. is already served by CoreDNS (user.server)
```

**Inspect.**

```sh
# the main Corefile
kubectl -n kube-system get configmap coredns -o jsonpath='{.data.Corefile}'

# which keys exist in coredns-custom, without dumping every value
kubectl -n kube-system get configmap coredns-custom \
  -o go-template='{{range $k, $v := .data}}{{$k}}{{"\n"}}{{end}}'

# one specific key
kubectl -n kube-system get configmap coredns-custom -o jsonpath='{.data.user\.server}'
```

**Remediation.** Decide who should own the zone. Either remove the zone from
the ZoneRoute, or — if the existing CoreDNS block is yours and obsolete —
remove that block from its own source. The route becomes accepted on the
next reconcile without any edit to the ZoneRoute object.

**Do not** delete or overwrite CoreDNS blocks you did not create. Other keys
of `coredns-custom` and other blocks of the Corefile belong to the
administrator or the platform, and removing them can break unrelated DNS.

### ZoneConflict

**Means.** Another **accepted** ZoneRoute already claims the same canonical
zone. Zones are compared case-insensitively and with the trailing dot
ignored, so `Example.COM`, `example.com` and `example.com.` are one zone. The
older route wins, by `metadata.creationTimestamp`; when timestamps are equal
— they have one-second resolution, so this is common — the tie-break is
resource name order. The message names the winner:

```
Zone example.com. is claimed by older ZoneRoute corporate
```

Parent and child zones are not a conflict: `example.com` and
`eu.example.com` can be owned by different routes, and CoreDNS applies the
longest match.

**Inspect.**

```sh
kubectl get zoneroutes -o custom-columns=\
NAME:.metadata.name,CREATED:.metadata.creationTimestamp,ZONES:.spec.zones
```

**Remediation.** Decide which route should own the zone. Usually the right
fix is to remove the duplicated zone from the newer route, or to merge the
two routes if they should share upstreams. Deleting the older route is also
possible but is rarely what you want — it is the one that is currently
working.

### Published: reason `Published`

**Means.** The route is accepted, its block is in `zoneroute.server`, and
the controller found the top-level `import custom/*.server` in the Corefile.

**Says nothing about** CoreDNS having loaded the fragment or about DNS
working — see [What Published=True does not
prove](#what-publishedtrue-does-not-prove).

### NotAccepted

**Means.** `Published=False` purely because `Accepted=False`. Nothing was
published for this route, and its block is absent from `zoneroute.server`.

**Remediation.** Troubleshoot the `Accepted` condition first; `Published`
follows automatically.

### IntegrationConfigMissing

**Means.** `kube-system/coredns-custom` does not exist, so there is nowhere
to publish. The message says so:

```
ConfigMap kube-system/coredns-custom does not exist; create it to enable publishing.
```

The controller **intentionally** does not create it, and its RBAC does not
allow `create` on ConfigMaps. Creating it is an installation decision:
on some platforms it already exists and carries provider-managed keys.

**Inspect.** Check whether the platform provides an equivalent object under
a different name, in which case the wiring is not the documented one:

```sh
kubectl -n kube-system get configmap
kubectl -n kube-system get configmap coredns -o jsonpath='{.data.Corefile}' | grep -n import
```

**Remediation.** If it is genuinely absent and you follow the documented
contract, create it empty:

```sh
kubectl -n kube-system create configmap coredns-custom
```

The controller notices and publishes within a reconcile.

**Do not** grant the controller permission to create ConfigMaps to work
around this, and do not delete or replace an existing `coredns-custom`.

### CoreDNSNotWired

**Means.** The fragment **was** written to `coredns-custom`, but the main
Corefile has no top-level `import custom/*.server`, so CoreDNS is not going
to load it:

```
Fragment written to kube-system/coredns-custom, but the Corefile has no
top-level "import custom/*.server" import.
```

This condition is about the import line only. The controller cannot see the
volume mount, the `reload` plugin or the CoreDNS Pod's filesystem, so
`CoreDNSNotWired` disappearing does not prove the rest of the contract is
satisfied.

**Inspect.**

```sh
kubectl -n kube-system get configmap coredns -o jsonpath='{.data.Corefile}' | grep -n 'import custom/\*\.server'
```

An import nested inside a server block does not count and is wrong: the
fragment contains complete server blocks.

**Remediation.** Establish the wiring as described in
[coredns-wiring.md](coredns-wiring.md#the-contract). The fragment is already
in place, so the route starts working as soon as CoreDNS picks up the
import.

### FragmentInvalid

**Means.** The controller rendered a fragment and its own structural
validation rejected it, so **nothing was written**. This is an internal
error: valid API input should always render to a parseable fragment.

The validation is structural — the fragment is re-parsed with the CoreDNS
Caddyfile parser — and is not a complete CoreDNS semantic validator. It
catches malformed block structure, not every possible configuration mistake.

**Inspect and report.**

```sh
kubectl get zoneroutes -o yaml > zoneroutes.yaml
kubectl -n zoneroute-system logs deployment/zoneroute-controller --tail=200
```

Please open an issue with those two outputs and the condition message; see
[Reporting a bug](#reporting-a-bug).

**Do not** hand-write `zoneroute.server` to compensate. The key is owned by
the controller and any edit is overwritten — see [Manual changes are
reverted](#manual-changes-are-reverted).

### WriteFailed

**Means.** The controller tried to patch `coredns-custom` and the Kubernetes
API server rejected the request. The message carries the API error:

```
Failed to publish fragment: patching ConfigMap kube-system/coredns-custom: ...
```

The reconcile returns an error, so it is retried with backoff; a transient
API problem clears on its own.

**Cause.** Usually RBAC (the Role or RoleBinding in `kube-system` was
modified or is missing), a ConfigMap that disappeared between the read and
the patch, or an admission webhook rejecting the patch.

**Inspect.**

```sh
kubectl -n zoneroute-system logs deployment/zoneroute-controller --tail=200
kubectl -n kube-system get configmap coredns-custom
kubectl -n kube-system get role zoneroute-coredns -o yaml
kubectl -n kube-system get rolebinding zoneroute-coredns -o yaml
```

Then check the permission directly — see [RBAC
problems](#rbac-problems).

**Remediation.** Reapply the installation manifests if the RBAC objects
drifted:

```sh
kubectl diff -f install.yaml
kubectl apply -f install.yaml
```

**Do not** grant the ServiceAccount broader permissions, and never
`cluster-admin`. The controller needs `patch` on exactly one ConfigMap.

## DNS does not resolve

Work down the chain; each step tells you whether to stop or continue.

**1. Status.** `kubectl get zoneroutes`. `ACCEPTED` False → [Accepted
reasons](#accepted-reasons). `PUBLISHED` False → [Published
reasons](#published-reasons). Columns empty or stale → [Status appears
stale](#status-appears-stale).

**2. The published fragment.**

```sh
kubectl -n kube-system get configmap coredns-custom -o jsonpath='{.data.zoneroute\.server}'
```

Your zone should appear in a block with the upstreams you configured. If the
block is missing or shows old upstreams, compare `status.observedGeneration`
with `metadata.generation` on the route.

**3. CoreDNS wiring.** The fragment being correct means nothing if CoreDNS
does not read it. Check all of it — the import, the volume, the mount path,
that it is not a `subPath` mount, and the `reload` plugin: [Fragment exists
but CoreDNS does not use it](#fragment-exists-but-coredns-does-not-use-it).

**4. CoreDNS logs and replicas.** See [CoreDNS reload
failures](#coredns-reload-failures). Query each CoreDNS Pod directly, not
only the Service: replicas reload independently, so one may answer correctly
while another has not picked up the change yet.

```sh
kubectl -n kube-system get pods -l k8s-app=kube-dns -o wide
kubectl run -it --rm dnsq --image=registry.k8s.io/e2e-test-images/agnhost:2.66.1 \
  --restart=Never -- dig +short @<coredns-pod-ip> host.example.com
```

**5. Upstream reachability.** If CoreDNS has loaded the fragment but returns
nothing, the upstream is next: [Upstream
connectivity](#upstream-connectivity).

**6. The client.** If a Pod still fails while `dig` from a debug Pod
succeeds, look at the client's own resolver configuration — `dnsPolicy`,
`dnsConfig`, and the `search`/`ndots` settings in its
`/etc/resolv.conf`:

```sh
kubectl exec <pod> -- cat /etc/resolv.conf
```

A query for a name with fewer dots than `ndots` is tried against the search
domains first; use a fully qualified name with a trailing dot to rule that
out.

## Fragment exists but CoreDNS does not use it

Check each point of the contract; any one of them is enough to stop the
fragment from taking effect.

```sh
# top-level import in the Corefile
kubectl -n kube-system get configmap coredns -o jsonpath='{.data.Corefile}' | grep -n 'import custom/\*\.server'

# the reload plugin
kubectl -n kube-system get configmap coredns -o jsonpath='{.data.Corefile}' | grep -n reload

# volume and mount: name, path /etc/coredns/custom, readOnly, and no subPath
kubectl -n kube-system get deployment coredns -o jsonpath='{.spec.template.spec.volumes}'
kubectl -n kube-system get deployment coredns -o jsonpath='{.spec.template.spec.containers[0].volumeMounts}'

# what the CoreDNS Pod actually sees
kubectl -n kube-system exec deployment/coredns -- ls -l /etc/coredns/custom
```

A `subPath` mount is the classic cause of "it worked once and never updated
again": with `subPath` the kubelet never refreshes the file, so CoreDNS
keeps the fragment it started with. The full contract, with the commands to
fix each point, is in [coredns-wiring.md](coredns-wiring.md#the-contract).

Propagation is asynchronous. A ConfigMap change reaches the Pod's filesystem
on the kubelet's sync period, and CoreDNS then notices it on its own reload
interval; both are configurable and the total delay is typically well under
two minutes. There is no exact convergence time to rely on — wait and
re-query rather than restarting CoreDNS.

## Upstream connectivity

ZoneRoute never checks that an upstream is reachable; it only writes what
you declared. Test from inside the cluster, against the upstream IP
directly, using the same image the project's e2e suite uses:

```sh
# UDP, the default transport
kubectl run -it --rm dnsq --image=registry.k8s.io/e2e-test-images/agnhost:2.66.1 \
  --restart=Never -- dig +short @10.10.10.53 host.example.com

# TCP, used for large answers and by some networks
kubectl run -it --rm dnsq --image=registry.k8s.io/e2e-test-images/agnhost:2.66.1 \
  --restart=Never -- dig +short +tcp @10.10.10.53 host.example.com

# a non-default upstream port
kubectl run -it --rm dnsq --image=registry.k8s.io/e2e-test-images/agnhost:2.66.1 \
  --restart=Never -- dig +short @10.10.10.54 -p 5353 host.example.com
```

Keep in mind how ZoneRoute declares upstreams:

- they are **IP addresses only**; a hostname never appears here;
- each upstream's port defaults to **53** unless you set `port`;
- the order is **sequential**: the first upstream is tried first, the next
  one on failure. A first upstream that is reachable but answers
  `SERVFAIL`/`NXDOMAIN` is still an answer — the list is failover, not
  a search path.

If the query times out, the problem is between the CoreDNS Pods and the
upstream, not in ZoneRoute. Test from a Pod on the same node as a CoreDNS
replica if you suspect node-level routing, and check whatever applies in
your environment: NetworkPolicy admitting egress to UDP and TCP 53, firewall
or security-group rules, routing and NAT, and whether the upstream itself
answers for that zone at all.

## CoreDNS reload failures

If CoreDNS cannot parse the configuration after a change, it logs the error
and **keeps serving the previous valid configuration**. Everything looks
healthy from ZoneRoute's side — the fragment is published — while CoreDNS is
still running yesterday's config.

```sh
kubectl -n kube-system logs -l k8s-app=kube-dns --prefix --tail=200 | grep -iE 'reload|restart failed|plugin'
kubectl -n kube-system get configmap coredns -o jsonpath='{.data.Corefile}'
kubectl -n kube-system get configmap coredns-custom -o jsonpath='{.data.zoneroute\.server}'
```

A successful reload logs that the configuration was reloaded; a failure logs
an error naming the offending line. Because the published fragment and any
other `*.server` key are parsed together with the Corefile, an unrelated
user-owned key with a syntax error also blocks the fragment from loading.

Where the CoreDNS Prometheus metrics endpoint is enabled and reachable in
your installation, the `coredns_reload_failed_total` counter is a convenient
signal for the same thing. It is optional, not part of the ZoneRoute
contract, and not exposed by every installation — logs and real DNS queries
remain the primary evidence.

ZoneRoute itself exposes no Kubernetes Events and no ZoneRoute-specific
metrics; there is nothing else on its side to look at.

## RBAC problems

The controller runs as:

```
system:serviceaccount:zoneroute-system:zoneroute-controller
```

Check its actual permissions with `kubectl auth can-i --as`. These should
all answer **yes**:

```sh
SA=system:serviceaccount:zoneroute-system:zoneroute-controller
kubectl auth can-i list   zoneroutes.dns.mihnk.org         --as=$SA
kubectl auth can-i watch  zoneroutes.dns.mihnk.org         --as=$SA
kubectl auth can-i update zoneroutes.dns.mihnk.org/status  --as=$SA
kubectl auth can-i list   configmaps -n kube-system        --as=$SA
kubectl auth can-i patch  configmaps/coredns-custom -n kube-system --as=$SA
```

and these should all answer **no**, by design:

```sh
kubectl auth can-i create configmaps -n kube-system         --as=$SA
kubectl auth can-i patch  configmaps/coredns -n kube-system --as=$SA
kubectl auth can-i update zoneroutes.dns.mihnk.org          --as=$SA
kubectl auth can-i get    secrets -n kube-system            --as=$SA
```

A `no` where a `yes` is expected explains `WriteFailed` or a controller that
sees no routes; reapply `install.yaml`. A `yes` where a `no` is expected
means the RBAC was widened outside this project — the controller does not
need it, and `patch` on `coredns` or `create` on ConfigMaps would let it
affect configuration it must never touch.

Do not resolve a permission problem by binding the ServiceAccount to
`cluster-admin`. The complete permission set is listed in
[install.md](install.md#what-gets-installed).

## Status appears stale

The controller fails closed. If it cannot read `kube-system/coredns` or the
`Corefile` key is missing, or the CoreDNS configuration cannot be parsed, it
returns an error and writes **no** status at all rather than guessing. The
conditions you see are then the last ones it was able to compute, and there
is no `Unknown` condition to signal it — the API exposes only `Accepted` and
`Published`.

```sh
# does the status describe the spec you last applied?
kubectl get zoneroute <name> -o jsonpath='{.metadata.generation} {.status.observedGeneration}{"\n"}'

# why the controller is not progressing
kubectl -n zoneroute-system logs deployment/zoneroute-controller --tail=200
kubectl -n zoneroute-system get deployment zoneroute-controller
```

Typical messages and what they mean:

| Log message | Cause |
| --- | --- |
| `reading ConfigMap kube-system/coredns` | the ConfigMap is missing or RBAC was changed |
| `ConfigMap kube-system/coredns has no "Corefile" key` | CoreDNS is configured from somewhere else; the installation contract does not hold |
| `inspecting CoreDNS configuration` | the Corefile or a `*.server` key does not parse |
| `listing ZoneRoutes` | API or RBAC problem |

Also make sure the controller is actually running and has the lease: with
`--leader-elect=true` (the default) only one replica reconciles.

## Manual changes are reverted

`data["zoneroute.server"]` is owned by the controller. It is recomputed from
the ZoneRoute objects on every reconcile, and the ConfigMap is watched, so a
hand edit is detected and overwritten within seconds — by design, and the
project's e2e suite asserts it.

Do not manage that key by hand. To change what it contains, change the
ZoneRoute objects. To stop the controller from managing it, uninstall it —
see [Uninstall](install.md#uninstall).

Every **other** key of `coredns-custom` is yours or your platform's. The
controller reads `*.server` keys to detect zones CoreDNS already serves and
never writes them; its RBAC allows `patch` on this one ConfigMap and the
patch it sends touches a single key.

As a troubleshooting step, never delete the whole `coredns-custom`
ConfigMap: it may carry provider or user configuration that has nothing to
do with ZoneRoute. If you want ZoneRoute's key gone, remove just that key:

```sh
kubectl -n kube-system patch configmap coredns-custom --type merge \
  -p '{"data":{"zoneroute.server":null}}'
```

(The controller will republish it while it is still installed.)

## Collecting diagnostics

What each source is good for:

- **controller logs** — Kubernetes API errors, Corefile inspection failures,
  publish failures, reconcile errors, leader election. One line saying
  Events are forbidden is expected and harmless: the controller is
  deliberately not granted Event permissions.
- **CoreDNS logs** — configuration parse and reload results, and per-query
  behaviour when the `log` plugin is enabled.
- **upstream logs** — only if your upstream resolver provides them; they are
  the fastest way to tell "the query never arrived" from "the answer was
  wrong".

A compact set to attach to a bug report:

```sh
kubectl version
kubectl get zoneroutes -o yaml
kubectl -n kube-system get configmap coredns -o jsonpath='{.data.Corefile}'
kubectl -n kube-system get configmap coredns-custom \
  -o go-template='{{range $k, $v := .data}}{{$k}}{{"\n"}}{{end}}'
kubectl -n kube-system get configmap coredns-custom -o jsonpath='{.data.zoneroute\.server}'
kubectl -n zoneroute-system logs deployment/zoneroute-controller --tail=500
kubectl -n kube-system logs -l k8s-app=kube-dns --prefix --tail=500
kubectl -n kube-system get deployment coredns \
  -o jsonpath='{.spec.template.spec.containers[0].image}{"\n"}'
kubectl -n zoneroute-system get deployment zoneroute-controller \
  -o jsonpath='{.spec.template.spec.containers[0].image}{"\n"}'
```

None of these read Secrets. Do not paste Secrets, kubeconfigs or credentials
into an issue.

## Reporting a bug

Include:

- [ ] Kubernetes version;
- [ ] CoreDNS version (the image from the command above);
- [ ] ZoneRoute version (the controller image);
- [ ] the ZoneRoute YAML and its full `status`;
- [ ] the CoreDNS `Corefile`;
- [ ] the key **names** in `coredns-custom`, plus the content of any
  `*.server` key relevant to the problem;
- [ ] controller logs;
- [ ] CoreDNS logs;
- [ ] what you did, what you expected, what happened.

Review this material before publishing it. Corefiles and `*.server` keys
describe internal DNS zones and resolver addresses, which many organisations
consider sensitive; redact what you must, and say that you did. Never
include Secrets, credentials, or private DNS records that are not needed to
reproduce the problem.
