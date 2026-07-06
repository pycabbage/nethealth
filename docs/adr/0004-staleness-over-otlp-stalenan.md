# 0004. Immediate-gap staleness for down targets via StaleNaN over OTLP

## Status
Accepted (2026-07)

## Context
When a target goes down the pinger has no real RTT to report. If `ping_duration_milliseconds`
simply stops being written, Mimir carries the last value forward for its query lookback-delta
(~5m), so a downed target's latency appears frozen at its pre-outage value for five minutes.
The pre-OTLP fix was to write a Prometheus StaleNaN sample. OTLP, however, has no staleness
primitive, and the OTel Go SDK's ObservableGauge cannot emit a `NoRecordedValue` data point —
it can only omit the observation (which reproduces the carry-forward).

## Decision
While a target is down, the pinger observes the Prometheus StaleNaN bit pattern
(`0x7ff0000000000002`) *as the gauge value* of `ping_duration_milliseconds`. The value flows
verbatim OTLP → Alloy (`otelcol.exporter.prometheus`) → `remote_write` → Mimir, which honors
it as a staleness marker ending the series at that timestamp. A query then shows an
**immediate gap** for the downed target. `ping_up` keeps reporting 0 (a fresh sample), so a
real outage stays distinguishable from a dead pipeline.

This was validated end-to-end (the "M1" spike) before adoption: emitting StaleNaN bits as the
gauge value produces an immediate gap in Mimir, whereas merely omitting the observation
reproduces the 5-minute carry-forward, and no Alloy setting (e.g. `gc_frequency`) synthesizes
a staleness marker on its own.

## Consequences
- This is the correct layer: the pinger is the only component that knows per-target up/down at
  emit time. Alloy's declarative pipeline cannot synthesize StaleNaN by joining `ping_up` and
  the duration series.
- The mechanism is Prometheus-wire specific — a StaleNaN gauge value only means "stale" to a
  Prometheus/Mimir backend. It is a pragmatic sentinel, not portable OTLP semantics.
- Any query of `ping_duration_milliseconds` (dashboard, Explore, alert) sees the gap directly;
  no query-time masking is required.
