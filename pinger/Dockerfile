# syntax=docker/dockerfile:1
# golang:1.25-alpine: the OTel Go SDK (otel v1.44+, otlpmetricgrpc/otlploggrpc)
# requires go >= 1.25, so 1.24 cannot build it.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY main.go ./
# go.sum is generated here because the host cannot reach the Go module proxy;
# `go mod tidy` in the build container resolves the full OTel dependency graph.
RUN go mod tidy
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /pinger .

FROM alpine:3.20
RUN apk add --no-cache libcap && adduser -D -u 10001 pinger
COPY --from=build /pinger /usr/local/bin/pinger
# Grant raw-socket capability to the binary so ICMP works as non-root.
# Effective at runtime only because the container is given NET_RAW via cap_add.
RUN setcap cap_net_raw+ep /usr/local/bin/pinger
USER pinger
ENTRYPOINT ["/usr/local/bin/pinger"]
