# 0005. OTLP → Alloy relay topology and metric label hygiene

## Status

Accepted (2026-07)

## Context

The pinger emits OTLP; the backends are Mimir (Prometheus remote_write) and Loki. Something
must translate OTLP into each backend's protocol, and the OTLP→Prometheus conversion adds
labels the dashboards do not expect.

## Decision

- **Topology**: pinger → OTLP/gRPC (metrics + logs) → Grafana Alloy. Alloy relays metrics via
  `otelcol.exporter.prometheus` → `prometheus.remote_write` → Mimir, and logs via
  `otelcol.exporter.loki` → `loki.write` → Loki. Mimir, Loki, and Grafana are unchanged by the
  migration.
- **Metric label hygiene**: each series must carry exactly
  `{__name__, target, provider, family, role}`.
  - `add_metric_suffixes = false` keeps `ping_up` / `ping_duration_milliseconds` verbatim.
  - `include_scope_info` / `include_scope_labels` / `include_target_info = false` suppress the
    `otel_scope_*` labels and the `target_info` metric.
  - a `prometheus.relabel` drops `job` (derived from resource `service.name`) and `instance`.
    These labels are *born in the OTLP→Prometheus conversion*, not at the source, so they are
    stripped at the same Alloy stage that creates them.
- **Loki stream labels**: the pinger tags semantic attributes (target/provider/family/role/
  event); Alloy decides which become indexed stream labels via the `loki.attribute.labels`
  hint. The pinger need not know Loki's label/cardinality model.

## Consequences

- Dashboards query the same low-cardinality series as before the migration; no query changes
  were needed.
- `service.name` is kept on the resource (useful for logs/telemetry) even though it is dropped
  as the metric `job` label.
- Backend-specific label concerns live in Alloy, where the extra labels arise, keeping the
  pinger free of Prometheus/Loki label knowledge.
