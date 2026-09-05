# muster

Calls the roll of container images on every Kubernetes node.

You give muster a **manifest**: a plain text file listing the images
your cluster must be able to start when its registries are
unreachable (a cold boot with the WAN down, an air-gapped site, an
edge box on a flaky uplink).  muster reconciles that manifest against
each node's reported image cache -- continuously, using only the
Kubernetes API -- and exports the discrepancies as Prometheus
metrics, in both directions an honest inventory needs:

- **Absent**: a manifested image a node does not hold.  If its
  registry is unreachable when the node next needs it, the workload
  will not start.  `muster_absent{node, image}`.
- **Unlisted**: an image *running* in a namespace you watch that the
  manifest does not list -- your ledger has drifted from reality,
  typically after a cluster upgrade swapped a system image.
  `muster_unlisted{namespace, image}`.

muster is the inventory clerk, not the ledger's author and not the
stockroom porter: how you *produce* the manifest (hand-written, helm
values, a generator that scans your labeled workloads) and how you
*fill* the caches (an image pre-pull DaemonSet, a mirror, boot media)
are yours.  It pairs naturally with a pre-pull DaemonSet whose
rollout health tells you pulls still *work*, while muster tells you
the results are still *present*.

## The manifest

One image reference per line; `#` comments and blank lines ignored.

```
# LAN core
docker.io/coredns/coredns:1.14.6@sha256:900f9c10...
quay.io/metallb/speaker:v0.16.0
```

Matching semantics:

- An entry pinned `...@sha256:<digest>` matches a node image carrying
  the **same digest**, whatever name it is known by.
- A bare `repo:tag` entry requires that **exact name** in the node's
  image list.

The manifest file is re-read on every reconcile pass, so mounting it
from a ConfigMap picks up changes without a restart.

## Running it

See `examples/kubernetes.yaml` for a complete, minimal deployment:
Namespace-agnostic RBAC (get/list on `nodes`, plus `pods` if you use
drift detection), a ConfigMap manifest, and a Deployment.
`examples/alerts.yaml` has starter Prometheus rules.

Flags:

| Flag | Default | Meaning |
|---|---|---|
| `-manifest` | (required) | path to the manifest file |
| `-listen` | `:9909` | metrics listener |
| `-interval` | `1m` | reconcile cadence |
| `-drift-namespaces` | (empty) | comma-separated namespaces whose RUNNING pod images must appear in the manifest; empty disables drift detection |
| `-version` | | print the name and exit |

## The kubelet truncation trap (read this)

muster reads node image lists from the Kubernetes API
(`node.status.images`) on purpose: no node agents, no
container-runtime sockets, no privileges.  The price: the kubelet
**truncates** that list to `nodeStatusMaxImages` entries (default
**50**), keeping the *largest* images -- so on a busy node your
smallest images (pause, tiny sidecars, scratch binaries) fall off the
report while still cached, and muster reads them as absent.

If your nodes hold more than ~50 images, raise `nodeStatusMaxImages`
in the kubelet config (e.g. 500; `-1` is unlimited).  muster exports
`muster_node_images_reported{node}` so you can see how close each
node sits to the cap; alert on it approaching your configured
maximum.

## Metrics

```
muster_reconcile_success              1 if the last pass completed
muster_reconcile_timestamp_seconds    when it ran
muster_manifest_entries               images the manifest expects
muster_node_images_reported{node}     entries the kubelet reported (see the trap above)
muster_absent_count{node}             manifested images missing from the node
muster_absent{node,image} 1           each missing image, named
muster_unlisted_count{namespace}      running-but-unmanifested images, per watched namespace
muster_unlisted{namespace,image} 1    each drift, named
```

## Design constraints

Standard library only; single static binary `FROM scratch`; reads the
in-cluster ServiceAccount credentials; the only write anywhere is the
metrics listener.  `-version` exists so image pre-pullers have a
no-op invocation that exits 0.
