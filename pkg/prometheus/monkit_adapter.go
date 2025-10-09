package prometheus

import (
	"sort"

	"github.com/prometheus/client_golang/prometheus"
	monkit "github.com/spacemonkeygo/monkit/v3"
)

type MonkitCollector struct {
	registry *monkit.Registry
}

func NewMonkitCollector(registry *monkit.Registry) *MonkitCollector {
	return &MonkitCollector{registry: registry}
}

func (c *MonkitCollector) Describe(ch chan<- *prometheus.Desc) {
	// No-op: dynamic metrics collection
}

func (c *MonkitCollector) Collect(ch chan<- prometheus.Metric) {
	metricsMap := make(map[string]prometheus.Metric)

	c.registry.Stats(func(key monkit.SeriesKey, field string, val float64) {
		labelNames := make([]string, 0, len(key.Tags.All())+1)
		labelValues := make([]string, 0, len(key.Tags.All())+1)

		// Collect and sort tags for consistent ordering
		if key.Tags != nil {
			tags := key.Tags.All()
			keys := make([]string, 0, len(tags))
			for k := range tags {
				keys = append(keys, k)
			}
			sort.Strings(keys)

			for _, k := range keys {
				labelNames = append(labelNames, k)
				labelValues = append(labelValues, tags[k])
			}
		}

		if field != "" {
			labelNames = append(labelNames, "field")
			labelValues = append(labelValues, field)
		}

		// Create unique identifier for deduplication
		metricID := key.Measurement
		for i, name := range labelNames {
			if i < len(labelValues) {
				metricID += "_" + name + "=" + labelValues[i]
			}
		}

		desc := prometheus.NewDesc(key.Measurement, key.Measurement, labelNames, nil)
		metric := prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, val, labelValues...)
		metricsMap[metricID] = metric
	})

	for _, metric := range metricsMap {
		ch <- metric
	}
}
