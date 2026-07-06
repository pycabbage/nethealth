# 0002. One reused ICMP socket per target

## Status
Accepted (2026-07)

## Context
An earlier design created and tore down a raw ICMP socket for every one-second probe. On
the Docker Desktop / WSL2 network stack this socket churn induced spurious per-second
timeouts, making healthy targets look flaky.

## Decision
Each target runs a single long-lived pinger whose ICMP socket is reused for the whole
process lifetime. The ping loop is restarted only if the socket setup itself fails.

## Consequences
- No per-probe socket setup/teardown, which eliminates the churn-induced spurious timeouts.
- Requires the NET_RAW capability (granted via `setcap cap_net_raw+ep` on the binary plus
  the compose `cap_add: NET_RAW`).
- A restart resets that target's transition-tracking baseline, so a rare socket-setup
  failure may drop a single state-change log — acceptable, since it only happens when
  probing isn't working at all.
