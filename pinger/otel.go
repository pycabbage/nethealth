package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"go.opentelemetry.io/otel/attribute"
	otlploggrpc "go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	otlpmetricgrpc "go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
)

// otelProviders bundles the metric and log providers so main can flush both together.
type otelProviders struct {
	meter  *sdkmetric.MeterProvider
	logger *sdklog.LoggerProvider
}

// setupOTEL builds the OTLP gRPC exporters and their providers pointed at endpoint.
func setupOTEL(ctx context.Context, endpoint string) (*otelProviders, error) {
	res, err := sdkresource.New(context.Background(),
		sdkresource.WithAttributes(attribute.String("service.name", serviceName())),
	)
	if err != nil {
		return nil, fmt.Errorf("resource init: %w", err)
	}

	// --- metrics: OTLP gRPC -> PeriodicReader(1s) -> MeterProvider ---
	metricExp, err := otlpmetricgrpc.New(ctx,
		otlpmetricgrpc.WithEndpoint(endpoint),
		otlpmetricgrpc.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("otlp metric exporter init: %w", err)
	}
	reader := sdkmetric.NewPeriodicReader(metricExp, sdkmetric.WithInterval(time.Second))
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(reader),
	)

	// --- logs: OTLP gRPC -> BatchProcessor -> LoggerProvider ---
	logExp, err := otlploggrpc.New(ctx,
		otlploggrpc.WithEndpoint(endpoint),
		otlploggrpc.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("otlp log exporter init: %w", err)
	}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp)),
	)

	return &otelProviders{meter: mp, logger: lp}, nil
}

// Shutdown flushes both providers, logging (but not failing on) either error.
func (p *otelProviders) Shutdown(ctx context.Context) {
	if err := p.meter.Shutdown(ctx); err != nil {
		log.Printf("meter provider shutdown: %v", err)
	}
	if err := p.logger.Shutdown(ctx); err != nil {
		log.Printf("logger provider shutdown: %v", err)
	}
}
