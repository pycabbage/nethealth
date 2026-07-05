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
	otlpmetricgrpc "go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	otlploggrpc "go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
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
	// A target is "up" if a reply arrived within this window. Being a little over
	// 2 intervals, it tolerates the occasional single dropped packet without
	// flapping, while still flagging a real outage within ~3s.
	upWindow = 2500 * time.Millisecond
	// Delay the first verdict so a reply (if reachable) has time to land, making
	// the first emitted sample the target's true state — so the consumer records a
	// correct baseline instead of logging a bogus down->up transition on startup.
	baselineGrace = 3 * time.Second
	// maxPlausibleRttMs bounds a believable round-trip time. pro-bing computes RTT
	// as receivedAt.Sub(sendTime) with no monotonic reading in the decoded payload,
	// so a backward wall-clock step can make RTT negative and a forward step absurd.
	// A value outside [0, maxPlausibleRttMs] is such an artifact, not a measurement.
	maxPlausibleRttMs = 60000 // 60s
)

// staleNaN is the IEEE-754 NaN payload Prometheus/Mimir interpret as a staleness
// marker (github.com/prometheus/prometheus/model/value.StaleNaN). Observed as the
// ping_duration_milliseconds gauge value while a target is down, it flows verbatim
// through OTLP -> Alloy (otelcol.exporter.prometheus) -> remote_write and ends the
// series at that timestamp in Mimir. A query then shows an IMMEDIATE gap for the
// downed target instead of carrying the last RTT forward for the lookback window
// (~5m) — this was validated end-to-end in the M1 staleness spike before the
// migration. ping_up keeps reporting 0 (a fresh sample) so an outage is still
// distinguishable from a dead pipeline.
var staleNaN = math.Float64frombits(0x7ff0000000000002)

// live holds each target's latest observation, keyed by target address. It is
// written by the per-target consumers (below) and read once per second by the
// OTel metrics callback. A target is absent until its baseline sample lands, so
// no series is emitted for it before then.
var (
	liveMu sync.Mutex
	live   = make(map[string]sample)
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	endpoint := otlpEndpoint()
	log.Printf("pinger starting: %d targets, OTLP -> %s (metrics+logs, gRPC insecure)", len(targets), endpoint)

	// Wait for Alloy's OTLP port to accept connections before wiring up the
	// exporters, so startup does not spam connection-refused errors.
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

	// One goroutine per target. Each runs a reused-socket ping loop (producer)
	// and a consumer that updates the shared state map read by the metrics
	// callback and emits a state-change log when reachability flips.
	for _, t := range targets {
		go runTarget(t, logger)
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

	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		liveMu.Lock()
		defer liveMu.Unlock()
		for _, t := range targets {
			s, ok := live[t.addr]
			if !ok {
				continue // no baseline observation yet -> emit no series
			}
			attrs := metric.WithAttributes(
				attribute.String("target", t.addr),
				attribute.String("provider", t.provider),
				attribute.String("family", t.family),
				attribute.String("role", t.role),
			)
			up := int64(0)
			dur := staleNaN // immediate-gap marker while down (see staleNaN)
			if s.up {
				up = 1
				dur = s.rtt
			}
			o.ObserveInt64(pingUp, up, attrs)
			o.ObserveFloat64(pingDur, dur, attrs)
		}
		return nil
	}, pingUp, pingDur)
	if err != nil {
		log.Fatalf("register metrics callback: %v", err)
	}
}

// runTarget owns one target end to end: a restartable ping loop feeding samples,
// a shared-state update per sample (for the metrics callback), and transition
// detection for state-change logs.
func runTarget(t target, logger otellog.Logger) {
	results := make(chan sample, 8)

	// Producer: a single long-lived pinger reused for the whole process lifetime.
	// Reusing one ICMP socket per target — instead of tearing one down every
	// second — avoids the spurious per-second timeouts that socket churn induced
	// on the Docker Desktop / WSL2 network stack. pingLoop only returns on a
	// socket setup error, so restart it if that ever happens.
	go func() {
		for {
			if err := pingLoop(t.addr, results); err != nil {
				log.Printf("ping loop for %s exited: %v (restarting)", t.addr, err)
			}
			time.Sleep(time.Second)
		}
	}()

	// Consumer: publish each sample into the shared state map (read by the metrics
	// callback) and emit a Loki-bound log only when reachability actually flips.
	// prev stays nil until the first sample so the baseline observation does not
	// emit a spurious "state change" on every process (re)start.
	var prev *bool
	for s := range results {
		liveMu.Lock()
		live[t.addr] = s
		liveMu.Unlock()

		if prev == nil {
			up := s.up
			prev = &up
			continue
		}
		if *prev != s.up {
			emitStateChange(logger, t, stateName(*prev), stateName(s.up))
			*prev = s.up
		}
	}
}

// pingLoop runs one persistent pinger for addr, sending an echo every second on a
// reused socket, and emits one reachability sample per second. Reachability is
// judged purely on "how recently did a reply arrive", independent of whether each
// send succeeded — so both an unreachable target (sends succeed, no reply) and a
// local network drop (sends fail, no reply) are detected.
func pingLoop(addr string, results chan<- sample) error {
	pinger, err := probing.NewPinger(addr)
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
		// Discard replies with an implausible RTT instead of recording them. The
		// reply is genuine (it passed pro-bing's ICMP-ID and UUID checks), but a
		// wall-clock adjustment between send and receive can make the derived RTT
		// negative or absurdly large (see maxPlausibleRttMs). Such a value must
		// neither be reported as a metric nor refresh lastRecv — leaving the
		// up-window untouched is safe because the upWindow debounce already
		// tolerates the occasional missed reply.
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
		emit := func() {
			mu.Lock()
			up := !lastRecv.IsZero() && time.Since(lastRecv) < upWindow
			rtt := lastRtt
			mu.Unlock()
			s := sample{up: up}
			if up {
				s.rtt = rtt
			}
			// Non-blocking: never let a slow consumer stall this loop. A dropped
			// sample only skips one collection tick.
			select {
			case results <- s:
			default:
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
		ep = "http://alloy:4317"
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
