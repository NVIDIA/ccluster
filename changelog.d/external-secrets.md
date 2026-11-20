### Added
- `catalogs/cluster_base/external_secrets` installs the External Secrets Operator
  (upstream chart 2.15.0): its CRDs, controller, webhook and cert-controller, with two
  controller and two webhook replicas. It creates no SecretStore or ClusterSecretStore.
  A cluster that already runs ESO leaves the chart out of its component set. The ip
  operator verifies ESO like every other cluster_base component, whoever installed it:
  `Verified` stays False until ESO serves `external-secrets.io/v1` and its webhook has a
  ready endpoint. The ip operator's ClusterRole now reads ValidatingWebhookConfigurations
  and ExternalSecrets.
