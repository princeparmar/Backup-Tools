package prometheus

import (
	"context"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	monkit "github.com/spacemonkeygo/monkit/v3"
)

type MetricType string

const (
	CounterType   MetricType = "counter"
	HistogramType MetricType = "histogram"
	GaugeType     MetricType = "gauge"
	TimerType     MetricType = "timer"
)

type Metrics struct {
	monkitRegistry *monkit.Registry
	promRegistry   *prometheus.Registry
	metrics        map[string]prometheus.Collector
	mu             sync.RWMutex

	// System metrics
	CPUUsage       prometheus.Gauge
	MemoryUsage    prometheus.Gauge
	GoroutineCount prometheus.Gauge
}

var (
	GlobalMetrics *Metrics
	Mon           = monkit.Package()
)

func NewMetrics() *Metrics {
	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	m := &Metrics{
		monkitRegistry: monkit.Default,
		promRegistry:   registry,
		metrics:        make(map[string]prometheus.Collector),
	}

	factory := promauto.With(registry)
	m.CPUUsage = factory.NewGauge(prometheus.GaugeOpts{
		Name: "system_cpu_usage_percent",
		Help: "Current CPU usage percentage",
	})
	m.MemoryUsage = factory.NewGauge(prometheus.GaugeOpts{
		Name: "system_memory_usage_bytes",
		Help: "Current memory usage in bytes",
	})
	m.GoroutineCount = factory.NewGauge(prometheus.GaugeOpts{
		Name: "system_goroutines_total",
		Help: "Current number of goroutines",
	})

	return m
}

func InitMetrics() {
	globalMu.Lock()
	defer globalMu.Unlock()
	if GlobalMetrics == nil {
		GlobalMetrics = NewMetrics()
	}
}

func (m *Metrics) GetPrometheusRegistry() *prometheus.Registry {
	if m == nil {
		return prometheus.DefaultRegisterer.(*prometheus.Registry)
	}
	return m.promRegistry
}

func (m *Metrics) GetMonkitRegistry() *monkit.Registry {
	if m == nil {
		return monkit.Default
	}
	return m.monkitRegistry
}

func CreateMetricsHandler() http.Handler {
	globalMu.RLock()
	defer globalMu.RUnlock()

	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	if GlobalMetrics != nil {
		registry.MustRegister(
			GlobalMetrics.CPUUsage,
			GlobalMetrics.MemoryUsage,
			GlobalMetrics.GoroutineCount,
		)

		GlobalMetrics.mu.RLock()
		for _, collector := range GlobalMetrics.metrics {
			registry.MustRegister(collector)
		}
		GlobalMetrics.mu.RUnlock()

		if GlobalMetrics.monkitRegistry != nil {
			registry.MustRegister(NewMonkitCollector(GlobalMetrics.monkitRegistry))
		}
	}

	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
}

func (m *Metrics) InstrumentOperation(operationName string, tags ...monkit.SeriesTag) func(context.Context) func(error) {
	if m == nil || m.monkitRegistry == nil {
		return noOpInstrumentation
	}

	task := m.monkitRegistry.Package().Task(append(tags,
		monkit.NewSeriesTag("operation", operationName))...)
	return func(ctx context.Context) func(error) {
		start := time.Now()
		return func(err error) {
			duration := time.Since(start)
			task(&ctx)(&err)
			status := "success"
			if err != nil {
				status = "error"
			}
			m.RecordOperation(operationName, status, duration)
		}
	}
}

func (m *Metrics) RecordMetric(metricName string, metricType MetricType, value float64, labels ...string) {
	if m == nil || m.monkitRegistry == nil {
		return
	}

	tags := labelsToTags(labels...)
	pkg := m.monkitRegistry.Package()

	switch metricType {
	case CounterType:
		pkg.Counter(metricName, tags...).Inc(int64(value))
	case HistogramType, GaugeType:
		pkg.FloatVal(metricName, tags...).Observe(value)
	case TimerType:
		pkg.FloatVal(metricName+"_duration_seconds", tags...).Observe(value)
	}
}

func (m *Metrics) RecordCounter(metricName string, value int64, labels ...string) {
	if m != nil {
		m.RecordMetric(metricName, CounterType, float64(value), labels...)
	}
}

func (m *Metrics) RecordHistogram(metricName string, value float64, labels ...string) {
	if m != nil {
		m.RecordMetric(metricName, HistogramType, value, labels...)
	}
}

func (m *Metrics) RecordGauge(metricName string, value float64, labels ...string) {
	if m != nil {
		m.RecordMetric(metricName, GaugeType, value, labels...)
	}
}

func (m *Metrics) RecordTimer(metricName string, duration time.Duration, labels ...string) {
	if m != nil {
		m.RecordMetric(metricName, TimerType, duration.Seconds(), labels...)
	}
}

func (m *Metrics) RecordOperation(operation, status string, duration time.Duration, labels ...string) {
	if m != nil {
		allLabels := append([]string{"operation", operation, "status", status}, labels...)
		m.RecordTimer("operation_duration_seconds", duration, allLabels...)
		m.RecordCounter("operation_total", 1, allLabels...)
	}
}

func (m *Metrics) RecordError(errorType, component string, labels ...string) {
	if m != nil {
		allLabels := append([]string{"error_type", errorType, "component", component}, labels...)
		m.RecordCounter("errors_total", 1, allLabels...)
	}
}

func (m *Metrics) RecordSize(operation string, size int64, labels ...string) {
	if m != nil {
		allLabels := append([]string{"operation", operation}, labels...)
		m.RecordHistogram("size_bytes", float64(size), allLabels...)
	}
}

func (m *Metrics) UpdateSystemMetrics() {
	if m == nil {
		return
	}

	m.GoroutineCount.Set(float64(runtime.NumGoroutine()))

	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	m.MemoryUsage.Set(float64(memStats.Alloc))

	// Simple CPU estimate based on goroutines
	cpuEstimate := float64(runtime.NumGoroutine()) * 0.1
	if cpuEstimate > 100 {
		cpuEstimate = 100
	}
	m.CPUUsage.Set(cpuEstimate)
}

func (m *Metrics) StartSystemMetricsCollection(ctx context.Context) {
	if m == nil {
		return
	}

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.UpdateSystemMetrics()
		}
	}
}

// Global convenience functions
func withGlobalMetrics(fn func(*Metrics)) {
	globalMu.RLock()
	defer globalMu.RUnlock()
	if GlobalMetrics != nil {
		fn(GlobalMetrics)
	}
}

func RecordMetric(metricName string, metricType MetricType, value float64, labels ...string) {
	withGlobalMetrics(func(m *Metrics) {
		m.RecordMetric(metricName, metricType, value, labels...)
	})
}

func RecordCounter(metricName string, value int64, labels ...string) {
	withGlobalMetrics(func(m *Metrics) {
		m.RecordCounter(metricName, value, labels...)
	})
}

func RecordHistogram(metricName string, value float64, labels ...string) {
	withGlobalMetrics(func(m *Metrics) {
		m.RecordHistogram(metricName, value, labels...)
	})
}

func RecordGauge(metricName string, value float64, labels ...string) {
	withGlobalMetrics(func(m *Metrics) {
		m.RecordGauge(metricName, value, labels...)
	})
}

func RecordTimer(metricName string, duration time.Duration, labels ...string) {
	withGlobalMetrics(func(m *Metrics) {
		m.RecordTimer(metricName, duration, labels...)
	})
}

func RecordOperation(operation, status string, duration time.Duration, labels ...string) {
	withGlobalMetrics(func(m *Metrics) {
		m.RecordOperation(operation, status, duration, labels...)
	})
}

func RecordError(errorType, component string, labels ...string) {
	withGlobalMetrics(func(m *Metrics) {
		m.RecordError(errorType, component, labels...)
	})
}

func RecordSize(operation string, size int64, labels ...string) {
	withGlobalMetrics(func(m *Metrics) {
		m.RecordSize(operation, size, labels...)
	})
}

func InstrumentOperation(operationName string, tags ...monkit.SeriesTag) func(context.Context) func(error) {
	globalMu.RLock()
	defer globalMu.RUnlock()
	if GlobalMetrics != nil {
		return GlobalMetrics.InstrumentOperation(operationName, tags...)
	}
	return noOpInstrumentation
}

func noOpInstrumentation(ctx context.Context) func(error) {
	return func(err error) {}
}

func labelsToTags(labels ...string) []monkit.SeriesTag {
	tags := make([]monkit.SeriesTag, 0, len(labels)/2)
	for i := 0; i < len(labels); i += 2 {
		if i+1 < len(labels) {
			tags = append(tags, monkit.NewSeriesTag(labels[i], labels[i+1]))
		}
	}
	return tags
}
