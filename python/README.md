# ccluster (Python)

Pydantic v2 models for the `ccluster.nvidia.com/v1alpha1` API and for the typed values of
every chart in the catalog. The package is **generated**: nothing in `ccluster/v1alpha1/`
or `ccluster/catalogs/cluster_base/` is written by hand.

```bash
uv add ccluster --path ../python      # or: pip install ./python
```

```python
from ccluster import catalogs
from ccluster.catalogs.cluster_base import gpu_operator, keda
from ccluster.v1alpha1 import CCLComponentSet, CCLComponentSetSpec, ObjectMeta

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
print(cs.model_dump(by_alias=True, exclude_none=True))   # kubectl apply -f this
```

Field names are snake_case with the wire name as the alias, so always dump with
`by_alias=True, exclude_none=True`. `exclude_none` is what keeps an unset optional out of
the values Helm sees while an explicit `False` stays in — the same distinction the Go
catalog makes with pointer fields.

## What is generated from what

| Python | Source | Via |
| --- | --- | --- |
| `ccluster/v1alpha1/*.py` | `config/crd/bases/*.yaml` (the CRD `openAPIV3Schema`) | `hack/pyschema` → JSON Schema → datamodel-code-generator |
| `ccluster/catalogs/cluster_base/<chart>/values.py` | `catalogs/cluster_base/<chart>/component.go` (`Values` and friends) | `hack/pyschema` (invopop/jsonschema reflection) → datamodel-code-generator |
| every `__init__.py` under those two | the same | `hack/pyschema` |

Three things are deliberately added on the way from the CRD: `apiVersion` and `kind` get
defaults, `metadata` becomes a small `ObjectMeta` (name, namespace, labels, annotations)
instead of an opaque dict, and a component's `values` is typed `dict[str, Any]` rather than
"anything". Everything else, including every validation constraint and doc comment, is the
CRD's.

`ccluster/__init__.py`, `ccluster/catalogs/__init__.py` (`entry`, `component`) and `tests/`
are hand-written.

## Regenerating

```bash
make python-generate     # from the repository root; needs Go and uv
make python-test
```

CI regenerates and fails on any diff, so edit the Go types or the CRD markers, regenerate,
and commit the result. Adding a chart to `catalogs/` means adding it to the table in
`hack/pyschema/main.go` too.
