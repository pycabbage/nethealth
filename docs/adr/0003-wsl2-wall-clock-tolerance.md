# 0003. Tolerating WSL2 / Docker Desktop wall-clock instability

## Status

Accepted (2026-07)

## Context

The WSL2 / Docker Desktop VM wall clock can step backward or forward (NTP correction, host
sleep/resume, VM time sync). This single root cause surfaces as three distinct symptoms
across the stack.

## Decision

Each layer absorbs the symptom where it appears:

- **pinger** discards implausible RTTs outside `[0, 60000ms]`. pro-bing derives RTT as
  `receivedAt.Sub(sendTime)`, and the send time it decodes from the packet payload carries
  no monotonic reading — so a backward step yields a negative RTT and a forward step an
  absurd one (an artifact, not a measurement). A discarded reply must also not refresh the
  up-window; the `upWindow` debounce (see ADR 0001) absorbs the occasional skipped reply.
- **Mimir** sets `out_of_order_time_window: 10m` so a backward step does not get freshly
  pushed samples rejected as out-of-order.
- **Loki** sets `reject_old_samples: false` so state-change lines stamped "now" are not
  rejected as too old after a jump.

## Consequences

- No monotonic-clock plumbing is added to the pinger (pro-bing does not expose the send
  timestamp). The value-range guard is a deliberate, documented trade-off rather than a
  deeper fix that would move fragility into seq→send-time bookkeeping (eviction on
  receive/loss, uint16 sequence wraparound).
- The storage tolerances are wider than a stable bare-metal host would need; acceptable for
  a local verification stack.
