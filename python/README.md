# ccluster (Python)

Pydantic v2 models for the `ccluster.nvidia.com/v1alpha1` API and for the typed values of
every chart in the catalog, plus a small client that registers clusters with the control
plane and deploys component sets onto them. The models are **generated**: nothing under
`ccluster/v1alpha1/` other than `base.py`, or under `ccluster/catalogs/` other than its
`__init__.py`, is written by hand.

```bash
uv add ccluster-py --path ../python   # or: pip install ./python; released: pip install ccluster-py
```

```python
from ccluster import catalogs
from ccluster.catalogs.cluster_base import gpu_operator, keda
from ccluster.v1alpha1 import CCLComponentSet, ObjectMeta
from ccluster.v1alpha1.cclcomponentset import CCLComponentSetSpec

cs = CCLComponentSet(
    metadata=ObjectMeta(name="local-base", namespace="ccluster-quickstart"),
    spec=CCLComponentSetSpec(
        components=[
            catalogs.component("keda", "ccluster-local", keda.CHART, "keda"),
            catalogs.component(
                "gpu-operator", "ccluster-local", gpu_operator.CHART, "gpu-operator",
                gpu_operator.Values(
                    gpu_operator=gpu_operator.ChartValues(
                        driver=gpu_operator.Toggle(enabled=False),
                    ),
                ),
            ),
        ]
    ),
)
print(cs.model_dump(mode="json", by_alias=True, exclude_none=True))   # kubectl apply -f this
```

Each kind's `Spec` and `Status`, the parts that differ between kinds, are generated from
the CRD into `ccluster.v1alpha1.<kind>`. `ccluster.v1alpha1.base` is hand-written and holds
what every kind shares: `ObjectMeta` (name, namespace, labels, annotations; server-managed
fields are kept when reading back) and the generic `CustomResource[Spec, Status]` envelope
with `apiVersion` fixed. `ccluster.v1alpha1` itself is generated from the CRD kinds: the
`Kind` enum and one concrete `CustomResource` per kind, `kind` fixed to its name.

Field names are snake_case with the wire name as the alias, so always dump with
`by_alias=True, exclude_none=True`. `exclude_none` is what keeps an unset optional out of
the values Helm sees while an explicit `False` stays in — the same distinction the Go
catalog makes with pointer fields.

## Creating an infra plane

Every `CCLClient` method takes the namespace it works in as its first argument and the
rest by keyword, so a namespace and a name cannot be passed in the wrong order. One client
serves every namespace; its constructor only runs API discovery, and by default it
connects with the in-cluster configuration of the pod it runs in. `apply_infraplane`
applies a Secret named `<name>-credentials` in Argo CD's cluster-secret shape (`server`,
and a `config` carrying the bearer token and the CA) and a `CCLInfraPlane` pointing at it;
`delete_infraplane` deletes the Secret and the CCLInfraPlane, the CCLInfraPlane first so
the operator's finalizer removes the mirrored Argo CD Secret, and leaves the namespace.
`ensure_namespace` creates the namespace, and is the only method that does: keeping it
separate means a mistyped namespace fails against the API server rather than quietly
gaining a working plane. Everything is server-side applied under the `ccluster-py` field
manager, so re-registering a cluster rotates its token in place. Both `apply_infraplane`
and `apply_component_set` return the applied object, a `CCLInfraPlane` or
`CCLComponentSet`, with the metadata the server filled in and the status the operator has
written so far.

```python
from ccluster.client import CCLClient

client = CCLClient()   # in-cluster
client.ensure_namespace("tenant-a")
plane = client.apply_infraplane(
    "tenant-a",
    name="edge-1",
    server="https://10.0.0.1:6443",
    bearer_token=open("token").read(),
    ca_cert=open("ca.crt").read(),
)
client.delete_infraplane("tenant-a", name="edge-1")
```

`server`, `bearer_token` and `ca_cert` are all optional. With none given,
`apply_infraplane` registers the cluster the operator itself runs in, credential-free —
Argo CD's special-cased in-cluster address, no ServiceAccount or token needed:

```python
plane = client.apply_infraplane("tenant-a", name="ccluster-local")
```

From a workstation, pass a client built from your kubeconfig:

```python
from kubernetes.config import new_client_from_config

client = CCLClient(api_client=new_client_from_config())
```

`examples/create_infraplane.py` is the same as a command. The call returns once the
objects are applied; `kubectl get cclinfraplane -n <namespace>` shows when the operator
has marked the plane `Available`.

## Deploying a component set

`apply_component_set` server-side applies a `CCLComponentSet` in the given namespace
from a list of components built with `catalogs.component`, each with a chart's typed
`Values` where the defaults are not wanted. Each component's `infraPlaneRef`
names the registered cluster it deploys to, so one set may span several planes; those
planes must be in the same namespace and already `Available`. List order is rollout order.
Because the set is applied under the same `ccluster-py` field manager, calling
`apply_component_set` again with a different list updates it in place. The namespace
argument is the set's own, on the control plane; a component's own namespace is on its
infra plane, created there by Argo CD from the `CreateNamespace=true` sync option the
operator puts on every Application.

```python
from ccluster import catalogs
from ccluster.catalogs.cluster_base import keda
from ccluster.client import CCLClient, component_set_current_conditions

client = CCLClient()
client.ensure_namespace("tenant-a")
client.apply_infraplane("tenant-a", name="ccluster-local")
cs = client.apply_component_set(
    "tenant-a",
    name="local-base",
    components=[
        catalogs.component(
            "keda", "ccluster-local", keda.CHART, "keda",
            keda.Values(keda=keda.ChartValues(operator=keda.Replicas(replica_count=1))),
        ),
    ],
)
finished = client.is_component_set_finished("tenant-a", name="local-base")
client.delete_component_set("tenant-a", name="local-base")
```

`apply_component_set` does not wait for rollout completion. Poll
`is_component_set_finished` with a caller-defined timeout before deploying dependent sets.
It reads once and requires `Validated=True`, `ComponentsApplied=True` and
`Progressing=False` for the current generation. It returns `False` during deletion and
raises `NotFoundError` for a missing set.

`component_set_current_conditions` says why a set is not finished yet. It keys its
conditions by type and drops the ones the operator computed from an older generation, so
what is left is its verdict on the spec as it now stands — after a failed validation the
other types still carry the previous generation's. `get_component_set` and
`get_infraplane` read one object and return it as its model, or raise `NotFoundError`.

```python
if not client.is_component_set_finished("tenant-a", name="local-base"):
    conditions = component_set_current_conditions(
        client.get_component_set("tenant-a", name="local-base")
    )
    for condition in conditions.values():
        print(condition.type, condition.status.value, condition.reason, condition.message)
```

A type is absent when the operator did not get far enough to write it: a set whose
validation failed has no `Progressing` condition for this generation.

`Progressing=False` reports Argo CD's health and sync verdict, which is about each
Application as Argo CD last compared it. Shortly after an update that verdict can still
be the previous spec's, so `True` means converged rather than proof that the newest values
are live.

`delete_component_set` returns as soon as the delete is accepted. The operator's finalizer
then uninstalls the set's charts in reverse order, one at a time, so the object stays in
`Terminating` until the last one is gone; a missing set is not an error, and the referenced
`CCLInfraPlane`s stay. `kubectl get cclcomponentset -n <namespace>` shows each set's
`Progress` column, which carries the `Progressing` condition's reason, and when a deleted
one has disappeared.
`examples/create_component_set.py` is the same as a command.

## What is generated from what

| Python | Source | Via |
| --- | --- | --- |
| `ccluster/v1alpha1/<kind>.py` (`<Kind>Spec`, `<Kind>Status` and their nested types) | `config/crd/bases/*.yaml`, the CRD's `spec` and `status` schemas | `hack/pyschema` → JSON Schema → datamodel-code-generator |
| `ccluster/v1alpha1/__init__.py` (`Kind`, `CCLComponentSet`, `CCLInfraPlane`) | the CRD kinds | `hack/pyschema`: one `Kind` member and one `CustomResource[Spec, Status]` subclass per kind |
| `ccluster/v1alpha1/base.py` (`API_VERSION`, `ObjectMeta`, `CustomResource`) | hand-written | the generic envelope template the concrete kinds specialise |
| `ccluster/catalogs/<catalog>/<chart>/__init__.py` (`Values` and friends, `CHART`) | `catalogs/<catalog>/<chart>/component.go`, via `catalogs/components.All` | `hack/pyschema` (invopop/jsonschema reflection) → datamodel-code-generator; `CHART` appended by the Makefile |

The CRD models carry controller-gen's constraints (name patterns, length and item bounds)
and docstrings, so a bad spec fails in Pydantic before it reaches the API server. Chart
values add nothing on the way from Go: optional scalars become `None` defaults.

`ccluster/__init__.py`, `ccluster/client.py` (`CCLClient`), `ccluster/catalogs/__init__.py`
(`entry`, `component`), `examples/` and `tests/` are hand-written.

## Regenerating

```bash
make python-generate     # from the repository root; needs Go and uv
make python-test
```

CI regenerates and fails on any diff, so edit the Go types or the CRD markers, regenerate,
and commit the result. A new CRD is picked up from `config/crd/bases`; a new chart from
`catalogs/components/components.go`. Neither needs a change to `hack/pyschema`.
