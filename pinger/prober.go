package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	probing "github.com/prometheus-community/pro-bing"

	otellog "go.opentelemetry.io/otel/log"
)

type target struct {
	addr     string
	provider string
	family   string
	role     string
}

// sample is one per-second reachability observation for a target.
type sample struct {
	up  bool
	rtt float64 // RTT in milliseconds; only meaningful when up.
}

const (
	// "up" if a reply arrived within this window. see docs/adr/0001-reachability-model.md
	upWindow = 2500 * time.Millisecond
	// Delay the first verdict so the baseline reflects true state. see docs/adr/0001-reachability-model.md
	baselineGrace = 3 * time.Second
	// Upper bound on a believable RTT. see docs/adr/0003-wsl2-wall-clock-tolerance.md
	maxPlausibleRttMs = 60000 // 60s
)

// reachabilityStore holds each target's latest observation; written by pingLoop emit
// tickers, read by the metrics callback.
type reachabilityStore struct {
	mu    sync.Mutex
	state map[string]sample
}

func newReachabilityStore() *reachabilityStore {
	return &reachabilityStore{state: make(map[string]sample)}
}

func (r *reachabilityStore) observe(addr string, s sample) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state[addr] = s
}

func (r *reachabilityStore) snapshot(addr string) (sample, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.state[addr]
	return s, ok
}

// evaluateReachability derives the per-second verdict from the last reply time and RTT.
// rtt is only carried when up. see docs/adr/0001-reachability-model.md
func evaluateReachability(lastRecv time.Time, lastRtt float64, now time.Time) sample {
	up := !lastRecv.IsZero() && now.Sub(lastRecv) < upWindow
	s := sample{up: up}
	if up {
		s.rtt = lastRtt
	}
	return s
}

// pingLoop runs one persistent pinger for t on a reused socket; its 1s emit ticker
// updates the store and logs reachability flips. see docs/adr/0001-reachability-model.md
func pingLoop(t target, logger otellog.Logger, store *reachabilityStore) error {
	pinger, err := probing.NewPinger(t.addr)
	if err != nil {
		return err
	}
	pinger.SetPrivileged(true) // raw ICMP socket; requires NET_RAW capability
	pinger.Interval = time.Second
	pinger.RecordRtts = false // long-running: don't accumulate RTT history

	var mu sync.Mutex
	var lastRecv time.Time // zero until the first reply for this loop
	var lastRtt float64
	pinger.OnRecv = func(pkt *probing.Packet) {
		rtt := float64(pkt.Rtt.Microseconds()) / 1000.0
		// Discard an implausible RTT without refreshing lastRecv. see docs/adr/0003-wsl2-wall-clock-tolerance.md
		if rtt < 0 || rtt > maxPlausibleRttMs {
			return
		}
		mu.Lock()
		lastRecv = time.Now()
		lastRtt = rtt
		mu.Unlock()
	}

	done := make(chan struct{})
	go func() {
		time.Sleep(baselineGrace)
		// prev is loop-local and nil until the baseline, so the first sample emits no
		// transition; a lingering ticker from a prior loop keeps its own prev.
		var prev *bool
		emit := func() {
			mu.Lock()
			recv := lastRecv
			rtt := lastRtt
			mu.Unlock()

			s := evaluateReachability(recv, rtt, time.Now())
			store.observe(t.addr, s)

			up := s.up
			if prev == nil {
				prev = &up
				return
			}
			if *prev != up {
				emitStateChange(logger, t, stateName(*prev), stateName(up))
				*prev = up
			}
		}
		emit() // baseline
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				emit()
			}
		}
	}()

	err = pinger.Run() // blocks until a fatal error (Count is unset = infinite)
	close(done)
	return err
}

// emitStateChange sends one state-change log record over OTLP, called only on a real
// flip so a healthy target produces no log noise.
func emitStateChange(logger otellog.Logger, t target, from, to string) {
	var r otellog.Record
	now := time.Now()
	r.SetTimestamp(now)
	r.SetObservedTimestamp(now)
	r.SetSeverity(otellog.SeverityInfo)
	r.SetBody(otellog.StringValue(
		fmt.Sprintf("state changed: %s -> %s (target=%s provider=%s)", from, to, t.addr, t.provider),
	))
	r.AddAttributes(
		otellog.String("target", t.addr),
		otellog.String("provider", t.provider),
		otellog.String("family", t.family),
		otellog.String("role", t.role),
		otellog.String("event", "state_change"),
	)
	logger.Emit(context.Background(), r)
	log.Printf("state change emitted: %s %s -> %s", t.addr, from, to)
}

func stateName(up bool) string {
	if up {
		return "up"
	}
	return "down"
}
