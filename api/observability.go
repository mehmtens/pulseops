package main

import (
	"context"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.30.0"
)

func initTelemetry(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return func(context.Context) error { return nil }, nil
	}
	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}
	res, err := telemetryResource(serviceName)
	if err != nil {
		return nil, err
	}
	provider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(res))
	otel.SetTracerProvider(provider)
	return provider.Shutdown, nil
}

func telemetryResource(serviceName string) (*resource.Resource, error) {
	return resource.Merge(resource.Default(), resource.NewSchemaless(semconv.ServiceName(serviceName), attribute.String("service.version", "3.0.0")))
}

func traceHTTPHandler(next http.Handler) http.Handler {
	return otelhttp.NewHandler(next, "pulseops.http", otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string { return r.Method + " " + r.URL.Path }))
}

func traceHTTPTransport() http.RoundTripper {
	return otelhttp.NewTransport(http.DefaultTransport)
}

func workerOfflineAfter() time.Duration {
	seconds, err := strconv.Atoi(env("PULSEOPS_WORKER_OFFLINE_AFTER_SECONDS", "30"))
	if err != nil || seconds < 5 || seconds > 3600 {
		seconds = 30
	}
	return time.Duration(seconds) * time.Second
}

func expectedRegions() []string {
	items := []string{}
	for _, item := range strings.Split(os.Getenv("PULSEOPS_EXPECTED_REGIONS"), ",") {
		item = strings.TrimSpace(item)
		if validRegion(item) && !slices.Contains(items, item) {
			items = append(items, item)
		}
	}
	slices.Sort(items)
	return items
}

func (a *app) listWorkers(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.Query(r.Context(), `SELECT region,started_at,last_seen_at,last_claimed_at,last_result_at,claims,results FROM worker_heartbeats ORDER BY region`)
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	defer rows.Close()
	now, threshold := time.Now().UTC(), workerOfflineAfter()
	workers, onlineRegions := []map[string]any{}, []string{}
	for rows.Next() {
		var region string
		var started, seen time.Time
		var claimed, result *time.Time
		var claims, results int64
		if err := rows.Scan(&region, &started, &seen, &claimed, &result, &claims, &results); err != nil {
			writeError(w, 500, "database error")
			return
		}
		online := now.Sub(seen) <= threshold
		if online {
			onlineRegions = append(onlineRegions, region)
		}
		workers = append(workers, map[string]any{"region": region, "startedAt": started, "lastSeenAt": seen, "lastClaimedAt": claimed, "lastResultAt": result, "claims": claims, "results": results, "online": online})
	}
	if err := rows.Err(); err != nil {
		writeError(w, 500, "database error")
		return
	}
	warnings := []string{}
	for _, region := range expectedRegions() {
		if !slices.Contains(onlineRegions, region) {
			warnings = append(warnings, "Expected region "+region+" is offline")
		}
	}
	if env("PULSEOPS_LOCAL_CHECKS", "true") == "false" && len(onlineRegions) < regionQuorum() {
		warnings = append(warnings, "Online worker regions are below the configured quorum")
	}
	writeJSON(w, 200, map[string]any{"workers": workers, "onlineRegions": len(onlineRegions), "requiredRegions": regionQuorum(), "offlineAfterSeconds": int(threshold.Seconds()), "warnings": warnings})
}
