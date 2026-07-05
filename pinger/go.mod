module nethealth/pinger

// OTel Go SDK (otlpmetricgrpc/otlploggrpc, sdk/metric v1.44+) requires go >= 1.25.
// The full dependency graph (OTel + gRPC + protobuf) is resolved by `go mod tidy`
// in the build container, which can reach the Go module proxy (the host cannot).
go 1.25

require github.com/prometheus-community/pro-bing v0.7.0
