# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import unittest

from catalog.cluster_base import (
    CertManager,
    ClusterComponents,
    Istio,
    IstioOverrides,
)


class TestClusterBaseDefaults(unittest.TestCase):
    def test_defaults_all_enabled(self):
        """No-arg construction must leave every component enabled — the
        controller includes all components unless explicitly disabled.
        """
        values = ClusterComponents()
        for field_name in ClusterComponents.model_fields:
            if field_name == "shared":
                continue
            self.assertTrue(getattr(values, field_name).enabled, field_name)

    def test_unset_overrides_excluded(self):
        """A component whose values is None must NOT emit a `values` key
        when dumped. The controller dict-merges shared + values; a bare
        empty `values` would clobber shared keys.
        """
        dumped = CertManager().model_dump(by_alias=True, exclude_none=True)
        self.assertNotIn("values", dumped)

    def test_set_overrides_emitted(self):
        """When values is populated with a per-component Overrides model,
        its (aliased) fields appear under the `values` key in the dump.
        """
        dumped = Istio(
            enabled=True,
            values=IstioOverrides.model_validate({"istio-cni": {"cni": {"cniBinDir": "/custom/path"}}}),
        ).model_dump(by_alias=True, exclude_none=True)
        self.assertEqual(dumped["values"], {"istio-cni": {"cni": {"cniBinDir": "/custom/path"}}})

    def test_classvars_excluded_from_dump(self):
        """chart_name / namespace / wave are ClassVars — they must not
        appear in serialized output (we'd send them to ArgoCD otherwise).
        """
        dumped = ClusterComponents().model_dump(by_alias=True, exclude_none=True)
        for key in ("chart_name", "namespace", "wave"):
            self.assertNotIn(key, dumped["cert_manager"])


if __name__ == "__main__":
    unittest.main()
