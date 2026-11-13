# cpa-quota-exporter

Prometheus quota exporter for [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI).

## Run

```sh
go build .
./cpa-quota-exporter \
  -listen 127.0.0.1:9108 \
  -cpa-url http://127.0.0.1:8317 \
  -management-key-file /var/lib/cliproxyapi/management-key
```

## Metrics

| Metric | Meaning |
| --- | --- |
| `llm_quota_fetch_success` | Latest account fetch succeeded: 1 or 0 |
| `llm_quota_remaining_ratio` | Remaining quota, 0–1 |
| `llm_quota_reset_timestamp_seconds` | Reset time in Unix seconds |
| `llm_quota_resets_available` | Available reset credits for Codex/Claude Code |
| `llm_quota_summary_value` | Credit/balance value with a `unit` label |
