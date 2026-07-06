# 0009. Native build stage with Go cross-compilation for multi-arch images

## Status

Accepted (2026-07)

## Context

CI builds the pinger image for `linux/amd64,linux/arm64` with `docker buildx` + QEMU
(`docker/setup-qemu-action`). With a plain `FROM golang:...` build stage, BuildKit runs the
*entire* build stage — `go mod download`, `go test ./...`, and `go build` — once per target
platform, and the arm64 pass executes under QEMU emulation on the amd64 runner. Emulating the
Go toolchain (compile + test) is extremely slow and dominates CI time, even though the produced
artifact is a single static, pure-Go binary that Go can cross-compile natively.

## Decision

Pin the build stage to the builder's native architecture and cross-compile the target binary:

- `FROM --platform=$BUILDPLATFORM golang:1.26.4-alpine AS build` — the toolchain always runs on
  the native runner arch (amd64), never under emulation.
- `go build` uses `CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH` to emit the target binary.
  CGO is off and the binary is pure Go, so no cross C toolchain is needed.
- `ARG TARGETOS`/`ARG TARGETARCH` are declared *after* `go mod download` and `go test ./...`, so
  those instructions stay byte-identical across platforms and BuildKit shares their layers —
  download and tests run once, not once per target.
- The runtime stage stays `FROM scratch`; it holds only the cross-compiled binary.
- `docker/setup-qemu-action` is deliberately kept in CI. SBOM generation (`sbom: true`) may still
  rely on QEMU, and that cannot be confirmed without a real CI run; the setup step costs only a
  few seconds, and with the native build stage no actual emulation of the build occurs.

## Consequences

- `go build`/`go test` run at native amd64 speed; the arm64 image no longer pays the QEMU
  emulation cost for compilation. Every `RUN` executes in the `$BUILDPLATFORM` container, so no
  arm64 container is created and QEMU is never invoked for the build — correctness by
  construction, not just observed timing.
- `go test ./...` effectively runs once per build (shared layer), not once per target platform.
  This is sound because the tested functions (reachability debounce, OTLP endpoint parsing) hold
  no architecture-dependent logic, so amd64-only test execution is sufficient.
- The build stage is no longer a literal "run everything on the target arch" model; anything
  that genuinely needed target-native execution (CGO, arch-specific codegen or tests) would have
  to be reintroduced deliberately. None applies to this pure-Go binary today.
