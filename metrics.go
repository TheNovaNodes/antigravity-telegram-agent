package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// SessionContextResetsTotal tracks silent context desync events where requested conversation ID was rejected by CLI runtime (#303).
	SessionContextResetsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "session_context_resets_total",
			Help: "Total count of silent context desyncs where requested conversation ID was rejected by CLI runtime.",
		},
		[]string{"bot"},
	)
)

func init() {
	RegisterEngineMetrics(prometheus.DefaultRegisterer)
}

// RegisterEngineMetrics registers engine metrics with the given Prometheus registerer.
func RegisterEngineMetrics(reg prometheus.Registerer) {
	if reg == nil {
		return
	}
	_ = reg.Register(SessionContextResetsTotal)
}

// RecordSessionContextReset increments the Prometheus counter for context desyncs.
func RecordSessionContextReset(botName string) {
	if botName == "" {
		botName = "unknown"
	}
	SessionContextResetsTotal.WithLabelValues(botName).Inc()
}

// StartMetricsServer boots an HTTP server exposing Prometheus metrics on the given address.
// If addr is empty, it checks the METRICS_ADDR environment variable (e.g. ":9090").
// If neither is specified, it returns nil without error.
func StartMetricsServer(addr string) (*http.Server, error) {
	if addr == "" {
		addr = os.Getenv("METRICS_ADDR")
	}
	if addr == "" {
		if port := os.Getenv("METRICS_PORT"); port != "" {
			addr = ":" + port
		}
	}
	if addr == "" {
		return nil, nil
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
	}

	go func() {
		log.Printf("[Metrics] Prometheus metrics exporter listening on http://%s/metrics", addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[Metrics] Server error: %v", err)
		}
	}()

	return server, nil
}

// StopMetricsServer gracefully halts the running metrics HTTP server.
func StopMetricsServer(server *http.Server) error {
	if server == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return server.Shutdown(ctx)
}
