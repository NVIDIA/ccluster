### Added
- `catalogs/resources/workload_secrets` has the External Secrets Operator create one
  deployment's Secrets from references into a secret store, so secret values never enter
  the values, the CCLComponentSet or the Argo CD Application. It takes `name`, `suffix`,
  `secretStoreRef` (`name`, `kind`), `env` (variable name to `{key, property, version}`)
  and `imagePull` (`server`, `username`, and `password` as a reference), and renders up
  to two ExternalSecrets, `<name>-env-<suffix>` and `<name>-pull-<suffix>`, with
  `refreshPolicy: CreatedOnce` and an immutable Secret; ESO builds the pull Secret's
  `.dockerconfigjson`. A workload chart reads the Secrets through `secretEnvVars` and
  `imagePullSecretNames`. Deploy it as a component named `secrets-<suffix>`, listed
  before the workload, and change the suffix with any reference: the cp operator writes
  the workload once the new ExternalSecrets are Ready, and deletes the old component, with
  its Secrets, after the workload is finished. The infra plane needs ESO serving
  `external-secrets.io/v1` (v0.16.0 or later) and the store.
- Its `Validate` requires `name`, `suffix`, the store's `name` and `kind` (`SecretStore`
  or `ClusterSecretStore`), each reference's `key`, and `imagePull`'s `server` and
  `username`. It rejects a suffix that is not a DNS label of at most 16 characters and an
  `env` name that is not a valid Secret key.
