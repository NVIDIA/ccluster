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

## Registering a cluster

`CCLClient` writes into one namespace, given at construction and created then if it does
not exist, and by default connects with the in-cluster configuration of the pod it runs
in. `register_infraplane` creates a Secret named `<name>-credentials` in Argo CD's
cluster-secret shape (`server`, and a `config` carrying the bearer token and the CA) and a
`CCLInfraPlane` pointing at it; `unregister_infraplane` deletes the Secret and the CCLInfraPlane, the
CCLInfraPlane first so the operator's finalizer removes the mirrored Argo CD Secret, and
leaves the namespace. Everything is server-side applied under the `ccluster-py` field
manager, so re-registering a cluster rotates its token in place. Both `register_infraplane`
and `create_component_set` return the applied object, a `CCLInfraPlane` or
`CCLComponentSet`, with the metadata the server filled in and the status the operator has
written so far.

```python
from ccluster.client import CCLClient

client = CCLClient("tenant-a")   # in-cluster
plane = client.register_infraplane(
    "edge-1",
    "https://10.0.0.1:6443",
    bearer_token=open("token").read(),
    ca_cert=open("ca.crt").read(),
)
client.unregister_infraplane("edge-1")
```

`server`, `bearer_token` and `ca_cert` are all optional. With none given,
`register_infraplane` registers the cluster the operator itself runs in, credential-free —
Argo CD's special-cased in-cluster address, no ServiceAccount or token needed:

```python
plane = client.register_infraplane("ccluster-local")
```

From a workstation, pass a client built from your kubeconfig:

```python
from kubernetes.config import new_client_from_config

client = CCLClient("tenant-a", api_client=new_client_from_config())
```

`examples/register_infraplane.py` is the same as a command. The call returns once the
objects are applied; `kubectl get cclinfraplane -n <namespace>` shows when the operator
has marked the plane `Available`.

## Deploying a component set

`create_component_set` server-side applies a `CCLComponentSet` in the client's namespace
from a list of components built with `catalogs.component`, each with a chart's typed
`Values` where the defaults are not wanted. Each component's `infraPlaneRef`
names the registered cluster it deploys to, so one set may span several planes; those
planes must be in the same namespace and already `Available`. List order is rollout order.
Because the set is applied under the same `ccluster-py` field manager, calling
`create_component_set` again with a different list updates it in place.

```python
from ccluster import catalogs
from ccluster.catalogs.cluster_base import keda
from ccluster.client import CCLClient

client = CCLClient("tenant-a")
client.register_infraplane("ccluster-local")
cs = client.create_component_set(
    "local-base",
    [
        catalogs.component(
            "keda", "ccluster-local", keda.CHART, "keda",
            keda.Values(keda=keda.ChartValues(operator=keda.Replicas(replica_count=1))),
        ),
    ],
)
client.delete_component_set("local-base")
```

`delete_component_set` returns as soon as the delete is accepted. The operator's finalizer
then uninstalls the set's charts in reverse order, one at a time, so the object stays in
`Terminating` until the last one is gone; a missing set is not an error, and the referenced
`CCLInfraPlane`s stay. `kubectl get cclcomponentset -n <namespace>` shows when the operator
has marked a set `ComponentsApplied` and when a deleted one has disappeared.
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
