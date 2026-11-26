### Changed
- **Breaking:** The cp chart rejects `external-dns.extraArgs` given as a list. Helm cannot
  merge a list into the chart's map of flags, so the list replaced them, with only a
  warning, and external-dns ran without `--txt-wildcard-replacement=wildcard`: it named a
  wildcard's TXT ownership record `a-*.nginx.<domain>`, which most DNS providers reject.
  Give extra flags as a map, such as `aws-zone-type: public`, which merges with the
  chart's by key.
