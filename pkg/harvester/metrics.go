package harvester

import (
	"github.com/prometheus/client_golang/prometheus"
)

var (
	// ExtractedArtifactsTotal tracks the total number of engineering artifacts harvested by taxonomy kind.
	ExtractedArtifactsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "agy_harvester_extracted_total",
			Help: "Total number of engineering markdown artifacts extracted by taxonomy kind.",
		},
		[]string{"kind"},
	)

	// SanitizedSecretsTotal tracks the cumulative number of secrets intercepted and redacted.
	SanitizedSecretsTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "agy_harvester_sanitized_secrets_total",
			Help: "Total number of secrets and credentials scrubbed from extracted artifacts.",
		},
	)

	// OrphanUncommittedGauge tracks the number of uncommitted artifacts older than 48 hours.
	OrphanUncommittedGauge = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "agy_harvester_orphan_uncommitted_count",
			Help: "Current number of orphaned artifacts older than 48 hours not committed to git.",
		},
	)
)

func init() {
	RegisterMetrics(prometheus.DefaultRegisterer)
}

// RegisterMetrics registers harvester metrics into the provided Prometheus registerer.
func RegisterMetrics(reg prometheus.Registerer) {
	if reg == nil {
		return
	}
	_ = reg.Register(ExtractedArtifactsTotal)
	_ = reg.Register(SanitizedSecretsTotal)
	_ = reg.Register(OrphanUncommittedGauge)
}

// RecordArtifact updates Prometheus counters for a single harvested artifact.
func RecordArtifact(art ExtractedArtifact) {
	kind := string(art.Kind)
	if kind == "" {
		kind = string(KindDoc)
	}
	ExtractedArtifactsTotal.WithLabelValues(kind).Inc()
	if art.RedactedCount > 0 {
		SanitizedSecretsTotal.Add(float64(art.RedactedCount))
	}
}

// RecordReport updates Prometheus counters from a complete HarvestReport.
func RecordReport(report *HarvestReport) {
	if report == nil {
		return
	}
	for _, art := range report.Artifacts {
		RecordArtifact(art)
	}
}

// SetOrphanCount updates the gauge tracking orphaned artifacts.
func SetOrphanCount(count int) {
	OrphanUncommittedGauge.Set(float64(count))
}
