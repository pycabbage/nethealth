# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this
repository.

## What this is

A Docker Compose observability stack that continuously monitors internet reachability. A Go
pinger probes 8 fixed targets (Google/Cloudflare DNS, IPv4+IPv6, primary+secondary) over raw
ICMP, emits OTLP metrics+logs to Grafana Alloy, which relays metrics to Mimir and logs to Loki,
both backed by RustFS (S3-compatible) object storage. Grafana visualizes the result.

## Commands

```sh
docker compose up -d --build      # build and start the whole stack
docker compose up -d --build pinger   # rebuild/restart just the pinger after a Go change
docker compose config --quiet     # validate compose.yml syntax/dependency graph (no side effects)
```

Grafana: <http://localhost:3456> (`admin` / `admin`).

The pinger has no local build/test tooling outside its Dockerfile — `go.mod`/`go.sum` are a
committed offline lockfile (see ADR 0006), so build it via the Docker image rather than a bare
`go build` unless module downloads are known to work in your environment. Table-driven tests for
the pure functions (reachability debounce, OTLP endpoint parsing) run via `go test ./...` in the
Docker build stage, so a failing test fails the build; no host Go toolchain is needed (same
offline-deps model as ADR 0006).

## Architecture

**Service dependency graph** (`compose.yml`): `rustfs` → `rustfs-init` (one-shot bucket
provisioning) → `mimir` / `loki` → `grafana` / `alloy` → `pinger`. Only `grafana` is published to
the host (3456→3000); everything else is internal to the compose network.

**Data flow**: `pinger` (Go, `pinger/*.go`) runs one persistent goroutine per target with a
reused raw ICMP socket, classifies each target up/down once per second, and pushes OTLP
metrics+logs over gRPC to `alloy`. Alloy (`alloy/config.alloy`) fans out: metrics via
`otelcol.exporter.prometheus` → `prometheus.remote_write` → Mimir; logs via
`otelcol.exporter.loki` → `loki.write` → Loki. Grafana queries both, provisioned from
`grafana/provisioning/` and `grafana/dashboards/nethealth.json`.

**Design rationale lives in `docs/adr/`, not in comments or README** — read the relevant ADR
before changing behavior it governs, since several non-obvious constraints span multiple files:

- 0001 reachability model: up/down is a debounced verdict (`upWindow=2500ms`), not a per-probe
  result.
- 0002 one reused ICMP socket per target for the process lifetime (socket churn caused spurious
  timeouts).
- 0003 WSL2/Docker Desktop wall-clock instability tolerance (RTT range guard in pinger;
  `out_of_order_time_window`/`reject_old_samples` in Mimir/Loki) — one root cause, three places
  it's absorbed.
- 0004 down targets are marked via a StaleNaN bit pattern written as the gauge value, not by
  omitting the observation, so Mimir shows an immediate gap instead of a 5-minute carry-forward.
- 0005 OTLP→Prometheus label hygiene (`job`/`instance`/scope labels) is stripped in Alloy, where
  those labels are born — the pinger stays backend-agnostic.
- 0006 Go 1.26.4, committed offline `go.mod`/`go.sum` lockfile; build container resolves deps,
  not the host.
- 0007 pinger image is `FROM scratch`, runs as root confined to `NET_RAW` only
  (`cap_drop: ALL` + `cap_add: NET_RAW`, `no-new-privileges`, `read_only`) — deliberately not
  non-root-on-alpine, since that would trade a smaller privilege set for a larger (distro)
  attack surface.
- 0008 RustFS buckets are provisioned by the `rustfs-init` one-shot service, not created
  manually — required because neither RustFS nor Mimir/Loki auto-create buckets.
- 0009 pinger build stage is pinned to `$BUILDPLATFORM` and cross-compiles via `GOOS`/`GOARCH`
  (CGO off), so `go test`/`go build` run on the native arch instead of per-target under QEMU.

## Known environment constraints

- This stack's usual host has no IPv6 internet connectivity at all (host and container level).
  The 4 IPv6 targets will permanently read `ping_up=0` there — that's environmental, not a
  pinger or compose bug.
- The pinger (via `pro-bing`) logs `FATAL: sending packet ... sendmsg: invalid argument` at
  ~1/sec during a real send failure (e.g. blackhole route). Despite the "FATAL:" prefix this
  does not crash the process — it's `pro-bing`'s per-send error logging, and it self-heals when
  reachability returns.

## Markdown hygiene

After editing any Markdown file (ADRs, this file, README), run
`bunx markdownlint-cli2 *.md docs/adr/*.md --fix` and resolve anything it reports.
`.markdownlint-cli2.jsonc` sets the line-length limit to 100 to match this repo's existing
wrapping convention (~90-95 chars), not the tool's 80-char default.

## Go lint hygiene

After editing any Go file under `pinger/`, run golangci-lint from that directory via Docker
(pinned to v2.12.2, matching the other pinned images) and resolve anything it reports:

```sh
docker run --rm -v "$(pwd)":/app -w /app golangci/golangci-lint:v2.12.2 golangci-lint run
```

Run it from `pinger/` (where `go.mod` lives); no host Go toolchain or golangci-lint install is
needed. On PowerShell, replace `$(pwd)` with `${PWD}`.
