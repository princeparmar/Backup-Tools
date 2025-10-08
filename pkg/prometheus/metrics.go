package prometheus

import (
	"context"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	monkit "github.com/spacemonkeygo/monkit/v3"
)

// MetricType represents the type of metric
type MetricType string

const (
	CounterType   MetricType = "counter"
	HistogramType MetricType = "histogram"
	GaugeType     MetricType = "gauge"
	TimerType     MetricType = "timer"
)

// MetricConfig holds configuration for a metric
type MetricConfig struct {
	Name        string
	Type        MetricType
	Help        string
	Labels      []string
	ConstLabels map[string]string
}

// Metrics holds all Prometheus metrics with Monkit integration
type Metrics struct {
	// Monkit registry for automatic instrumentation
	monkitRegistry *monkit.Registry

	// System metrics
	CPUUsage       prometheus.Gauge
	MemoryUsage    prometheus.Gauge
	GoroutineCount prometheus.Gauge

	// Generic metric storage
	metrics map[string]prometheus.Collector
	mu      sync.RWMutex
}

// Global metrics instance
var (
	GlobalMetrics *Metrics
	metricsMu     sync.RWMutex
)

// NewMetrics creates a new Metrics instance with Monkit integration
func NewMetrics() *Metrics {
	return &Metrics{
		monkitRegistry: monkit.Default,
		metrics:        make(map[string]prometheus.Collector),

		CPUUsage: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "system_cpu_usage_percent",
			Help: "Current CPU usage percentage",
		}),

		MemoryUsage: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "system_memory_usage_bytes",
			Help: "Current memory usage in bytes",
		}),

		GoroutineCount: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "system_goroutines_total",
			Help: "Current number of goroutines",
		}),
	}
}

// InitMetrics initializes the global metrics instance
func InitMetrics() {
	metricsMu.Lock()
	defer metricsMu.Unlock()
	if GlobalMetrics == nil {
		GlobalMetrics = NewMetrics()
	}
}

// GetMonkitRegistry returns the monkit registry for Prometheus collection
func (m *Metrics) GetMonkitRegistry() *monkit.Registry {
	if m == nil {
		return monkit.Default
	}
	return m.monkitRegistry
}

// Generic instrumentation method using Monkit
func (m *Metrics) InstrumentOperation(operationName string, tags ...monkit.SeriesTag) func(context.Context) func(error) {
	if m == nil || m.monkitRegistry == nil {
		return func(ctx context.Context) func(error) {
			return func(err error) {}
		}
	}

	task := m.monkitRegistry.Package().Task(append(tags,
		monkit.NewSeriesTag("operation", operationName))...)
	return func(ctx context.Context) func(error) {
		start := time.Now()
		return func(err error) {
			duration := time.Since(start)
			task(&ctx)(&err)
			// Also record as a Prometheus metric
			status := "success"
			if err != nil {
				status = "error"
			}
			m.RecordOperation(operationName, status, duration)
		}
	}
}

// Generic metric recording methods
func (m *Metrics) RecordMetric(metricName string, metricType MetricType, value float64, labels ...string) {
	if m == nil || m.monkitRegistry == nil {
		return
	}

	tags := labelsToTags(labels...)

	switch metricType {
	case CounterType:
		m.monkitRegistry.Package().Counter(metricName, tags...).Inc(int64(value))
	case HistogramType, GaugeType:
		m.monkitRegistry.Package().FloatVal(metricName, tags...).Observe(value)
	case TimerType:
		// For TimerType, we record the duration value directly as a histogram
		// This allows us to record pre-calculated durations
		m.monkitRegistry.Package().FloatVal(metricName+"_duration_seconds", tags...).Observe(value)
	}
}

// Convenience methods for specific metric types
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

// Generic operation recording methods
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

// UpdateSystemMetrics updates system-level metrics
func (m *Metrics) UpdateSystemMetrics() {
	if m == nil {
		return
	}

	m.GoroutineCount.Set(float64(runtime.NumGoroutine()))

	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	m.MemoryUsage.Set(float64(memStats.Alloc))

	// CPU usage would require more complex implementation
	// For now, we'll calculate a simple CPU usage based on goroutines
	cpuEstimate := float64(runtime.NumGoroutine()) * 0.1
	if cpuEstimate > 100 {
		cpuEstimate = 100
	}
	m.CPUUsage.Set(cpuEstimate)
}

// StartSystemMetricsCollection starts periodic system metrics collection
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

// Global convenience functions for generic metrics
func RecordMetric(metricName string, metricType MetricType, value float64, labels ...string) {
	metricsMu.RLock()
	defer metricsMu.RUnlock()
	if GlobalMetrics != nil {
		GlobalMetrics.RecordMetric(metricName, metricType, value, labels...)
	}
}

func RecordCounter(metricName string, value int64, labels ...string) {
	metricsMu.RLock()
	defer metricsMu.RUnlock()
	if GlobalMetrics != nil {
		GlobalMetrics.RecordCounter(metricName, value, labels...)
	}
}

func RecordHistogram(metricName string, value float64, labels ...string) {
	metricsMu.RLock()
	defer metricsMu.RUnlock()
	if GlobalMetrics != nil {
		GlobalMetrics.RecordHistogram(metricName, value, labels...)
	}
}

func RecordGauge(metricName string, value float64, labels ...string) {
	metricsMu.RLock()
	defer metricsMu.RUnlock()
	if GlobalMetrics != nil {
		GlobalMetrics.RecordGauge(metricName, value, labels...)
	}
}

func RecordTimer(metricName string, duration time.Duration, labels ...string) {
	metricsMu.RLock()
	defer metricsMu.RUnlock()
	if GlobalMetrics != nil {
		GlobalMetrics.RecordTimer(metricName, duration, labels...)
	}
}

func RecordOperation(operation, status string, duration time.Duration, labels ...string) {
	metricsMu.RLock()
	defer metricsMu.RUnlock()
	if GlobalMetrics != nil {
		GlobalMetrics.RecordOperation(operation, status, duration, labels...)
	}
}

func RecordError(errorType, component string, labels ...string) {
	metricsMu.RLock()
	defer metricsMu.RUnlock()
	if GlobalMetrics != nil {
		GlobalMetrics.RecordError(errorType, component, labels...)
	}
}

func RecordSize(operation string, size int64, labels ...string) {
	metricsMu.RLock()
	defer metricsMu.RUnlock()
	if GlobalMetrics != nil {
		GlobalMetrics.RecordSize(operation, size, labels...)
	}
}

// InstrumentOperation provides global instrumentation
func InstrumentOperation(operationName string, tags ...monkit.SeriesTag) func(context.Context) func(error) {
	metricsMu.RLock()
	defer metricsMu.RUnlock()
	if GlobalMetrics != nil {
		return GlobalMetrics.InstrumentOperation(operationName, tags...)
	}
	return func(ctx context.Context) func(error) {
		return func(err error) {}
	}
}

// CreateMetricsHandler creates a standard Prometheus metrics handler
func CreateMetricsHandler() http.Handler {
	return promhttp.Handler()
}

// Helper functions
func labelsToTags(labels ...string) []monkit.SeriesTag {
	tags := make([]monkit.SeriesTag, 0, len(labels)/2)
	for i := 0; i < len(labels); i += 2 {
		if i+1 < len(labels) {
			tags = append(tags, monkit.NewSeriesTag(labels[i], labels[i+1]))
		}
	}
	return tags
}
