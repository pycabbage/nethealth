# 0001. Reachability judged by reply-recency with debounce

## Status

Accepted (2026-07)

## Context

The pinger classifies each target as up or down once per second, over ordinary internet
links and a Docker Desktop / WSL2 network stack where isolated packet drops are normal. A
naive "last send failed → down" or "this probe got no immediate reply → down" rule flaps on
every single dropped packet.

## Decision

- A target is **up** iff a reply arrived within `upWindow = 2500ms` (~2 probe intervals).
  This debounces the occasional single dropped packet without flapping, while still flagging
  a real outage within ~3s.
- The first verdict is delayed by `baselineGrace = 3s`, so the initial emitted sample
  reflects the target's true state and the consumer records a correct baseline instead of
  logging a bogus down→up transition on every (re)start.
- Reachability is judged purely on *how recently a reply arrived*, independent of whether
  each send succeeded. So both an unreachable target (sends succeed, no reply) and a local
  uplink drop (sends fail, no reply) are detected as down.

## Consequences

- Up/down is a derived verdict over a sliding window, not a per-probe boolean; a genuine
  outage surfaces ~3s after the last reply.
- The `ping_up` metric already encodes this debounced verdict, so dashboards and alerts
  inherit the same debounce for free.
