package main

import (
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"engine/pkg/harvester"
)

func TestStartMetricsServer_DisabledWhenEmpty(t *testing.T) {
	t.Setenv("METRICS_ADDR", "")
	t.Setenv("METRICS_PORT", "")

	srv, err := StartMetricsServer("")
	if err != nil {
		t.Fatalf("Expected nil error for empty addr, got: %v", err)
	}
	if srv != nil {
		t.Errorf("Expected nil server when no address specified")
	}
}

func TestStartMetricsServer_HealthzAndPrometheus(t *testing.T) {
	// Allocate free ephemeral port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to bind ephemeral port: %v", err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()

	srv, err := StartMetricsServer(addr)
	if err != nil {
		t.Fatalf("StartMetricsServer failed: %v", err)
	}
	if srv == nil {
		t.Fatalf("Expected non-nil server")
	}
	defer func() {
		_ = StopMetricsServer(srv)
	}()

	// Allow server goroutine to start listening
	time.Sleep(100 * time.Millisecond)

	// 1. Check /healthz
	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("Failed to request /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200 from /healthz, got: %d", resp.StatusCode)
	}

	// 2. Trigger harvester metric recording
	harvester.RecordArtifact(harvester.ExtractedArtifact{
		Kind:          harvester.KindADR,
		RedactedCount: 3,
	})
	harvester.SetOrphanCount(7)

	// 3. Check /metrics
	metricsResp, err := http.Get("http://" + addr + "/metrics")
	if err != nil {
		t.Fatalf("Failed to request /metrics: %v", err)
	}
	defer metricsResp.Body.Close()

	bodyBytes, err := io.ReadAll(metricsResp.Body)
	if err != nil {
		t.Fatalf("Failed to read /metrics body: %v", err)
	}
	body := string(bodyBytes)

	if !strings.Contains(body, "agy_harvester_extracted_total") {
		t.Errorf("Expected agy_harvester_extracted_total in /metrics output, got: %s", body)
	}
	if !strings.Contains(body, "agy_harvester_sanitized_secrets_total") {
		t.Errorf("Expected agy_harvester_sanitized_secrets_total in /metrics output, got: %s", body)
	}
	if !strings.Contains(body, "agy_harvester_orphan_uncommitted_count") {
		t.Errorf("Expected agy_harvester_orphan_uncommitted_count in /metrics output, got: %s", body)
	}
}
