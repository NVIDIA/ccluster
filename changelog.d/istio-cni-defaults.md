### Changed
- **Breaking:** `cluster_base/istio` defaults `istio-cni` to the standard CNI paths,
  `cniConfDir: /etc/cni/net.d` and `cniBinDir: /opt/cni/bin`, which suit EKS, kind and
  kubeadm. The previous k3s defaults left `istio-cni-node` unready, and the component
  Degraded, on every other cluster. On k3s set `cniConfDir` to
  `/var/lib/rancher/k3s/agent/etc/cni/net.d` and `cniBinDir` to
  `/var/lib/rancher/k3s/data/cni` (`istio.CNIConfDirK3s`, `istio.CNIBinDirK3s`); on GKE
  COS set `cniBinDir` to `/home/kubernetes/bin` (`istio.CNIBinDirGKE`).
