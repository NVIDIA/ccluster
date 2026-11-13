### Added
- `catalogs/cluster_base/external_secrets` installs the External Secrets Operator
  (upstream chart 2.15.0): its CRDs, controller, webhook and cert-controller, with two
  controller and two webhook replicas. It creates no SecretStore or ClusterSecretStore.
  It is optional, and `cluster_base.Components()` leaves it out: install it where
  workloads read Secrets through ExternalSecrets, unless the cluster already runs ESO.
  The ip operator checks ESO only where it is installed, whoever installed it:
  `Verified` stays False until ESO serves `external-secrets.io/v1` and its webhook has a
  ready endpoint. The ip operator's ClusterRole now reads ValidatingWebhookConfigurations
  and ExternalSecrets.
