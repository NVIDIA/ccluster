### Fixed
- `ccluster register` keeps the infra-plane context's `tls-server-name` in the credentials Secret (`config.tlsClientConfig.serverName`), so Argo CD and the operator check the API server's certificate against the same name kubectl does.
