### Changed
- **Breaking:** `cluster_base/istio` defaults `istio-cni` to the standard CNI paths,
  `cniConfDir: /etc/cni/net.d` and `cniBinDir: /opt/cni/bin`, which suit EKS, kind and
  kubeadm. The previous k3s defaults left `istio-cni-node` unready, and the component
  Degraded, on every other cluster. On k3s set both paths to the k3s ones
  (`istio.CNIConfDirK3s`, `istio.CNIBinDirK3s`); on GKE COS set `cniBinDir` to
  `/home/kubernetes/bin` (`istio.CNIBinDirGKE`).
