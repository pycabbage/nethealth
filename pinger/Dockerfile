# syntax=docker/dockerfile:1
# Build constraints (go 1.26.4) and offline lockfile: see docs/adr/0006-build-constraints-go125-offline.md
FROM golang:1.26.4-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN go test ./...
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /pinger .

# Minimal runtime: the static binary only, no userland. Runs as root, but the container grants only NET_RAW. see docs/adr/0007-non-root-runtime-file-capability.md
FROM scratch
COPY --from=build /pinger /pinger
ENTRYPOINT ["/pinger"]
