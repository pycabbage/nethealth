# syntax=docker/dockerfile:1
# Build constraints (go >= 1.25) and the committed offline lockfile: see
# docs/adr/0006-build-constraints-go125-offline.md.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /pinger .

FROM alpine:3.20
RUN apk add --no-cache libcap && adduser -D -u 10001 pinger
COPY --from=build /pinger /usr/local/bin/pinger
# Grant raw-socket capability to the binary so ICMP works as non-root.
# Effective at runtime only because the container is given NET_RAW via cap_add.
RUN setcap cap_net_raw+ep /usr/local/bin/pinger
USER pinger
ENTRYPOINT ["/usr/local/bin/pinger"]
