package monitor

import (
	"sort"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	monkit "github.com/spacemonkeygo/monkit/v3"
)

// MonkitAdapter adapts Monkit metrics to Prometheus format
type MonkitAdapter struct {
	registry *monkit.Registry
}

// NewMonkitAdapter creates a new Monkit to Prometheus adapter
func NewMonkitAdapter(registry *monkit.Registry) *MonkitAdapter {
	return &MonkitAdapter{registry: registry}
}

// Describe implements prometheus.Collector interface (no-op for dynamic metrics)
func (a *MonkitAdapter) Describe(ch chan<- *prometheus.Desc) {
	// Dynamic metrics collection - no fixed description
}

// Collect converts Monkit metrics to Prometheus metrics
func (a *MonkitAdapter) Collect(ch chan<- prometheus.Metric) {
	collectedMetrics := make(map[string]prometheus.Metric)

	a.registry.Stats(func(key monkit.SeriesKey, field string, value float64) {
		// Filter out high-cardinality fields to reduce metric explosion
		if a.shouldSkipField(field) {
			return
		}

		labelNames := make([]string, 0, len(key.Tags.All())+1)
		labelValues := make([]string, 0, len(key.Tags.All())+1)

		// Process and sort tags for consistent ordering
		if key.Tags != nil {
			tags := key.Tags.All()
			tagKeys := make([]string, 0, len(tags))
			for k := range tags {
				tagKeys = append(tagKeys, k)
			}
			sort.Strings(tagKeys)

			for _, k := range tagKeys {
				labelNames = append(labelNames, k)
				labelValues = append(labelValues, tags[k])
			}
		}

		// Add field as a label if present (but only for essential fields)
		if field != "" && a.isEssentialField(field) {
			labelNames = append(labelNames, "field")
			labelValues = append(labelValues, field)
		}

		// Create metric description
		desc := prometheus.NewDesc(
			key.Measurement,
			key.Measurement,
			labelNames,
			nil,
		)

		metric := prometheus.MustNewConstMetric(
			desc,
			prometheus.GaugeValue,
			value,
			labelValues...,
		)

		// Create unique identifier to avoid duplicates
		metricID := a.generateMetricID(key.Measurement, labelNames, labelValues)
		collectedMetrics[metricID] = metric
	})

	// Send all collected metrics to the channel
	for _, metric := range collectedMetrics {
		ch <- metric
	}
}

// shouldSkipField determines if a field should be skipped to reduce cardinality
func (a *MonkitAdapter) shouldSkipField(field string) bool {
	// Skip detailed percentile and statistical fields that create high cardinality
	skipFields := map[string]bool{
		"r10":    true,
		"r50":    true,
		"r90":    true,
		"r99":    true,
		"ravg":   true,
		"rmin":   true,
		"rmax":   true,
		"recent": true,
		"high":   true,
		"low":    true,
	}
	return skipFields[field]
}

// isEssentialField determines if a field is essential and should be included as a label
func (a *MonkitAdapter) isEssentialField(field string) bool {
	essentialFields := map[string]bool{
		"count":     true,
		"sum":       true,
		"value":     true,
		"current":   true,
		"errors":    true,
		"successes": true,
		"failures":  true,
		"total":     true,
	}
	return essentialFields[field]
}

// generateMetricID creates a unique identifier for deduplication
func (a *MonkitAdapter) generateMetricID(measurement string, names, values []string) string {
	var id strings.Builder
	id.WriteString(measurement)

	for i, name := range names {
		if i < len(values) {
			id.WriteString("_")
			id.WriteString(name)
			id.WriteString("=")
			id.WriteString(values[i])
		}
	}
	return id.String()
}
