package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	probing "github.com/prometheus-community/pro-bing"

	"go.opentelemetry.io/otel/attribute"
	otlploggrpc "go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	otlpmetricgrpc "go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
)

type target struct {
	addr     string
	provider string
	family   string
	role     string
}

var targets = []target{
	{"8.8.8.8", "google", "ipv4", "primary"},
	{"8.8.4.4", "google", "ipv4", "secondary"},
	{"2001:4860:4860::8888", "google", "ipv6", "primary"},
	{"2001:4860:4860::8844", "google", "ipv6", "secondary"},
	{"1.1.1.1", "cloudflare", "ipv4", "primary"},
	{"1.0.0.1", "cloudflare", "ipv4", "secondary"},
	{"2606:4700:4700::1111", "cloudflare", "ipv6", "primary"},
	{"2606:4700:4700::1001", "cloudflare", "ipv6", "secondary"},
}

// sample is one per-second reachability observation for a target.
type sample struct {
	up  bool
	rtt float64 // RTT in milliseconds; only meaningful when up.
}

const (
	// A target is "up" if a reply arrived within this window; the >2-interval
	// width debounces a single dropped packet. see docs/adr/0001-reachability-model.md
	upWindow = 2500 * time.Millisecond
	// Delay the first verdict so the baseline sample reflects the true state and
	// does not log a bogus startup transition. see docs/adr/0001-reachability-model.md
	baselineGrace = 3 * time.Second
	// Bound a believable RTT; a value outside [0, maxPlausibleRttMs] is a
	// wall-clock artifact, not a measurement. see docs/adr/0003-wsl2-wall-clock-tolerance.md
	maxPlausibleRttMs = 60000 // 60s
)

// staleNaN is the IEEE-754 NaN payload Prometheus/Mimir read as a staleness
// marker (value.StaleNaN): observed as ping_duration_milliseconds while a target
// is down, it ends the series so a query shows an immediate gap (ping_up still
// reports 0). see docs/adr/0004-staleness-over-otlp-stalenan.md
var staleNaN = math.Float64frombits(0x7ff0000000000002)

// live holds each target's latest observation, keyed by target address. It is
// written by each target's emit ticker (in pingLoop) and read once per second by
// the OTel metrics callback. A target is absent until its baseline sample lands,
// so no series is emitted for it before then.
var (
	liveMu sync.Mutex
	live   = make(map[string]sample)
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	endpoint := otlpEndpoint()
	log.Printf("pinger starting: %d targets, OTLP -> %s (metrics+logs, gRPC insecure)", len(targets), endpoint)

	waitForEndpoint(endpoint)

	res, err := sdkresource.New(context.Background(),
		sdkresource.WithAttributes(attribute.String("service.name", serviceName())),
	)
	if err != nil {
		log.Fatalf("resource init: %v", err)
	}

	// --- metrics: OTLP gRPC -> PeriodicReader(1s) -> MeterProvider ---
	metricExp, err := otlpmetricgrpc.New(ctx,
		otlpmetricgrpc.WithEndpoint(endpoint),
		otlpmetricgrpc.WithInsecure(),
	)
	if err != nil {
		log.Fatalf("otlp metric exporter init: %v", err)
	}
	reader := sdkmetric.NewPeriodicReader(metricExp, sdkmetric.WithInterval(time.Second))
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(reader),
	)
	registerMetrics(mp.Meter("nethealth-pinger"))

	// --- logs: OTLP gRPC -> BatchProcessor -> LoggerProvider ---
	logExp, err := otlploggrpc.New(ctx,
		otlploggrpc.WithEndpoint(endpoint),
		otlploggrpc.WithInsecure(),
	)
	if err != nil {
		log.Fatalf("otlp log exporter init: %v", err)
	}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp)),
	)
	logger := lp.Logger("nethealth-pinger")

	// One restart loop per target, each running a reused-socket ping loop (see
	// docs/adr/0002-reused-icmp-socket.md). pingLoop's emit ticker updates the
	// shared state map read by the metrics callback and logs a state-change on a
	// reachability flip; it returns only on a socket setup error, so restart it.
	for _, t := range targets {
		go func(t target) {
			for {
				if err := pingLoop(t, logger); err != nil {
					log.Printf("ping loop for %s exited: %v (restarting)", t.addr, err)
				}
				time.Sleep(time.Second)
			}
		}(t)
	}

	<-ctx.Done()
	log.Printf("shutdown signal received; flushing OTLP providers")
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := mp.Shutdown(shutCtx); err != nil {
		log.Printf("meter provider shutdown: %v", err)
	}
	if err := lp.Shutdown(shutCtx); err != nil {
		log.Printf("logger provider shutdown: %v", err)
	}
}

// registerMetrics creates the two observable gauges and one callback that, once
// per collection (1s), reads every target's latest state and observes ping_up
// (0/1 always) and ping_duration_milliseconds (real RTT while up, the StaleNaN
// staleness marker while down) with the target/provider/family/role attributes.
func registerMetrics(meter metric.Meter) {
	pingUp, err := meter.Int64ObservableGauge("ping_up")
	if err != nil {
		log.Fatalf("ping_up gauge: %v", err)
	}
	pingDur, err := meter.Float64ObservableGauge("ping_duration_milliseconds")
	if err != nil {
		log.Fatalf("ping_duration_milliseconds gauge: %v", err)
	}

	// Precompute each target's immutable attribute set once; otherwise the
	// callback rebuilt it on every 1s tick.
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
		liveMu.Lock()
		defer liveMu.Unlock()
		for _, t := range targets {
			s, ok := live[t.addr]
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

// pingLoop runs one persistent pinger for t, sending an echo every second on a
// reused socket. Once per second its emit ticker updates the shared state map
// (read by the metrics callback) and logs a state-change on a reachability flip.
// Reachability is judged on how recently a reply arrived; see
// docs/adr/0001-reachability-model.md.
func pingLoop(t target, logger otellog.Logger) error {
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
		// Discard an implausible RTT, and don't refresh lastRecv (a wall-clock
		// artifact, not a measurement). see docs/adr/0003-wsl2-wall-clock-tolerance.md
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
		// prev stays nil until the baseline sample so the first observation does
		// not emit a spurious state change on process/loop (re)start. It is owned
		// by this closure; a lingering ticker from a prior pingLoop keeps its own.
		var prev *bool
		emit := func() {
			mu.Lock()
			up := !lastRecv.IsZero() && time.Since(lastRecv) < upWindow
			rtt := lastRtt
			mu.Unlock()

			s := sample{up: up}
			if up {
				s.rtt = rtt
			}
			liveMu.Lock()
			live[t.addr] = s
			liveMu.Unlock()

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

// emitStateChange sends a single state-change log record over OTLP. Alloy promotes
// the target/provider/family/role/event attributes to Loki stream labels, so the
// event is queryable as {event="state_change"}. Called only on a real flip, so a
// healthy target produces no log noise.
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

func serviceName() string {
	if s := os.Getenv("OTEL_SERVICE_NAME"); s != "" {
		return s
	}
	return "nethealth-pinger"
}

// otlpEndpoint returns the Alloy OTLP gRPC target as a bare host:port (the form
// otlp*grpc.WithEndpoint wants), honoring OTEL_EXPORTER_OTLP_ENDPOINT and
// defaulting to alloy:4317. The scheme is stripped; the exporters use insecure.
func otlpEndpoint() string {
	ep := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if ep == "" {
		ep = "alloy:4317"
	}
	ep = strings.TrimPrefix(ep, "http://")
	ep = strings.TrimPrefix(ep, "https://")
	return strings.TrimSuffix(ep, "/")
}

// waitForEndpoint blocks until a TCP connection to hostport succeeds, so the OTLP
// exporters are not created against a not-yet-listening Alloy.
func waitForEndpoint(hostport string) {
	for {
		conn, err := net.DialTimeout("tcp", hostport, 2*time.Second)
		if err == nil {
			_ = conn.Close()
			log.Printf("otlp endpoint reachable (%s)", hostport)
			return
		}
		time.Sleep(2 * time.Second)
	}
}
