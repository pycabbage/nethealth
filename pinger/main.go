package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	endpoint := otlpEndpoint()
	log.Printf("pinger starting: %d targets, OTLP -> %s (metrics+logs, gRPC insecure)", len(targets), endpoint)

	waitForEndpoint(endpoint)

	providers, err := setupOTEL(ctx, endpoint)
	if err != nil {
		log.Fatal(err)
	}

	store := newReachabilityStore()
	registerMetrics(providers.meter.Meter("nethealth-pinger"), store)
	logger := providers.logger.Logger("nethealth-pinger")

	// One restart loop per target running a reused-socket ping loop; pingLoop returns
	// only on a socket setup error, so restart it. see docs/adr/0002-reused-icmp-socket.md
	for _, t := range targets {
		go func(t target) {
			for {
				if err := pingLoop(t, logger, store); err != nil {
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
	providers.Shutdown(shutCtx)
}
