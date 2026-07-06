# syntax=docker/dockerfile:1
# Build constraints (go 1.26.4) and offline lockfile: see docs/adr/0006-build-constraints-go125-offline.md
# Build stage runs on the native builder arch; targets are cross-compiled. see docs/adr/0009-cross-compile-build-stage.md
FROM --platform=$BUILDPLATFORM golang:1.26.4-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN go test ./...
# ARG after the test layer keeps go mod download + go test identical across platforms so BuildKit shares them.
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /pinger .

# Minimal runtime: the static binary only, no userland. Runs as root, but the container grants only NET_RAW. see docs/adr/0007-non-root-runtime-file-capability.md
FROM scratch
COPY --from=build /pinger /pinger
ENTRYPOINT ["/pinger"]
