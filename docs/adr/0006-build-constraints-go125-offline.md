# 0006. Build constraints: Go 1.25 and offline module resolution

## Status

Accepted (2026-07)

## Context

The OTel Go SDK (otel v1.44+, otlpmetric/otlplog gRPC) requires Go ≥ 1.25. The build host
cannot reach the Go module proxy, though a build *container* can.

## Decision

- The build stage uses `golang:1.26.4-alpine`.
- `go.mod` and `go.sum` are committed as a complete lockfile (generated once inside a build
  container with `go mod tidy`). The image build then runs `go mod download` against the
  committed lockfile followed by `go build`, rather than re-resolving the dependency graph
  with `go mod tidy` on every build.

## Consequences

- Reproducible builds: dependencies are pinned in the committed lockfile, not re-resolved per
  build.
- Docker layer caching works as intended — editing `main.go` no longer busts the dependency
  layer, so `go mod download` is a cache hit on source-only changes.
- Updating dependencies is a deliberate step (regenerate the lockfile in a build container and
  commit it), not silent per-build drift.

## Update (2026-07-06)

Go was updated to 1.26.4: the `go.mod` `go` directive and the build-stage image tag
(`golang:1.26.4-alpine`) both follow. The decision here is unchanged — the committed offline
lockfile build model still holds; only the pinned toolchain advanced (the OTel SDK's Go ≥ 1.25
floor is unaffected).
