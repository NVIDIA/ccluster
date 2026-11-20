### Added
- The ip operator verifies `cert_manager`: `Verified` stays False until a
  ValidatingWebhookConfiguration registers the webhook `webhook.cert-manager.io` with an
  injected CA bundle and the Service it calls has a ready endpoint. The webhook is found
  by that entry name and followed to its Service, so cert-manager passes under any
  release name or namespace. The ip operator's ClusterRole now reads
  ValidatingWebhookConfigurations.
