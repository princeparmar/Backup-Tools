package restore

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	restoreJobsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "restore_jobs_total",
		Help: "Restore jobs by terminal status and method",
	}, []string{"method", "status"})

	restoreItemsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "restore_items_total",
		Help: "Restore items processed per method and outcome",
	}, []string{"method", "outcome"})
)

func recordJobTerminal(method, status string) {
	restoreJobsTotal.WithLabelValues(method, status).Inc()
}

func recordBatchItems(method string, processed, failed uint) {
	if processed > 0 {
		restoreItemsTotal.WithLabelValues(method, "success").Add(float64(processed))
	}
	if failed > 0 {
		restoreItemsTotal.WithLabelValues(method, "failed").Add(float64(failed))
	}
}
