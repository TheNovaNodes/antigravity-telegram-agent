package harvester

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestMetrics_RecordArtifactAndReport(t *testing.T) {
	art1 := ExtractedArtifact{
		Kind:          KindADR,
		RedactedCount: 2,
	}
	art2 := ExtractedArtifact{
		Kind:          KindResearch,
		RedactedCount: 0,
	}

	report := &HarvestReport{
		Artifacts: []ExtractedArtifact{art1, art2},
	}

	RecordReport(report)

	// Verify counter values via prometheus client_model
	var metric dto.Metric
	if err := SanitizedSecretsTotal.Write(&metric); err != nil {
		t.Fatalf("Failed to write metric: %v", err)
	}
	if metric.GetCounter().GetValue() < 2 {
		t.Errorf("Expected SanitizedSecretsTotal >= 2, got %f", metric.GetCounter().GetValue())
	}

	// Verify OrphanUncommittedGauge
	SetOrphanCount(5)
	var gaugeMetric dto.Metric
	if err := OrphanUncommittedGauge.Write(&gaugeMetric); err != nil {
		t.Fatalf("Failed to write gauge metric: %v", err)
	}
	if gaugeMetric.GetGauge().GetValue() != 5 {
		t.Errorf("Expected OrphanUncommittedGauge = 5, got %f", gaugeMetric.GetGauge().GetValue())
	}
}

func TestRegisterMetrics_NilSafe(t *testing.T) {
	// Should not panic on nil
	RegisterMetrics(nil)

	// Can register into custom registry
	reg := prometheus.NewRegistry()
	RegisterMetrics(reg)
}
