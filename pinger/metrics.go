package main

import (
	"context"
	"log"
	"math"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// staleNaN is the StaleNaN payload emitted as ping_duration_milliseconds while a target is down. see docs/adr/0004-staleness-over-otlp-stalenan.md
var staleNaN = math.Float64frombits(0x7ff0000000000002)

// registerMetrics registers the ping_up and ping_duration_milliseconds observable
// gauges and a 1s callback that observes each target's latest state from store.
func registerMetrics(meter metric.Meter, store *reachabilityStore) {
	pingUp, err := meter.Int64ObservableGauge("ping_up")
	if err != nil {
		log.Fatalf("ping_up gauge: %v", err)
	}
	pingDur, err := meter.Float64ObservableGauge("ping_duration_milliseconds")
	if err != nil {
		log.Fatalf("ping_duration_milliseconds gauge: %v", err)
	}

	// Precompute each target's immutable attribute set once, not per 1s tick.
	attrSets := make(map[string]attribute.Set, len(targets))
	for _, t := range targets {
		attrSets[t.addr] = attribute.NewSet(
			attribute.String("target", t.addr),
			attribute.String("provider", t.provider),
			attribute.String("family", t.family),
			attribute.String("role", t.role),
		)
	}

	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		for _, t := range targets {
			s, ok := store.snapshot(t.addr)
			if !ok {
				continue // no baseline observation yet -> emit no series
			}
			opt := metric.WithAttributeSet(attrSets[t.addr])
			up := int64(0)
			dur := staleNaN // immediate-gap marker while down (see staleNaN)
			if s.up {
				up = 1
				dur = s.rtt
			}
			o.ObserveInt64(pingUp, up, opt)
			o.ObserveFloat64(pingDur, dur, opt)
		}
		return nil
	}, pingUp, pingDur)
	if err != nil {
		log.Fatalf("register metrics callback: %v", err)
	}
}
