package monitor

import (
	"fmt"
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

// MetricsManager handles both Prometheus and Monkit metrics
type MetricsManager struct {
	monRegistry   *monkit.Registry
	promRegistry  *prometheus.Registry
	customMetrics map[string]prometheus.Collector
	mu            sync.RWMutex

	// System resource metrics
	SystemCPUUsage    prometheus.Gauge
	SystemMemoryUsage prometheus.Gauge
	GoroutineCount    prometheus.Gauge
}

var (
	globalManager *MetricsManager
	Mon           = monkit.Package()
	managerMutex  sync.RWMutex
)

// NewMetricsManager creates a new metrics manager instance
func NewMetricsManager() *MetricsManager {
	registry := prometheus.NewRegistry()

	// Register standard collectors
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	manager := &MetricsManager{
		monRegistry:   monkit.Default,
		promRegistry:  registry,
		customMetrics: make(map[string]prometheus.Collector),
	}

	// Create system metrics
	factory := promauto.With(registry)
	manager.SystemCPUUsage = factory.NewGauge(prometheus.GaugeOpts{
		Name: "system_cpu_usage_percent",
		Help: "Current CPU usage percentage",
	})
	manager.SystemMemoryUsage = factory.NewGauge(prometheus.GaugeOpts{
		Name: "system_memory_usage_bytes",
		Help: "Current memory usage in bytes",
	})
	manager.GoroutineCount = factory.NewGauge(prometheus.GaugeOpts{
		Name: "system_goroutines_total",
		Help: "Current number of goroutines",
	})

	// Register Monkit adapter
	registry.MustRegister(NewMonkitAdapter(manager.monRegistry))

	return manager
}

// InitializeGlobalManager initializes the global metrics manager (thread-safe)
func InitializeGlobalManager() error {
	managerMutex.Lock()
	defer managerMutex.Unlock()

	if globalManager == nil {
		globalManager = NewMetricsManager()
	}
	return nil
}

// GetGlobalManager returns the global metrics manager instance
func GetGlobalManager() *MetricsManager {
	managerMutex.RLock()
	defer managerMutex.RUnlock()
	return globalManager
}

// RegisterCustomMetric registers a custom metric with the manager
func (m *MetricsManager) RegisterCustomMetric(name string, collector prometheus.Collector) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if metric already exists
	if _, exists := m.customMetrics[name]; exists {
		return fmt.Errorf("metric %s already registered", name)
	}

	// Register the metric with the Prometheus registry
	if err := m.promRegistry.Register(collector); err != nil {
		return fmt.Errorf("failed to register metric %s: %w", name, err)
	}

	// Store the metric reference
	m.customMetrics[name] = collector
	return nil
}

// GetCustomMetric retrieves a custom metric by name
func (m *MetricsManager) GetCustomMetric(name string) prometheus.Collector {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.customMetrics[name]
}

// RegisterGlobalCustomMetric registers a custom metric with the global manager
func RegisterGlobalCustomMetric(name string, collector prometheus.Collector) error {
	managerMutex.RLock()
	defer managerMutex.RUnlock()

	if globalManager == nil {
		return fmt.Errorf("global metrics manager not initialized")
	}

	return globalManager.RegisterCustomMetric(name, collector)
}

// GetGlobalCustomMetric retrieves a custom metric by name from the global manager
func GetGlobalCustomMetric(name string) prometheus.Collector {
	managerMutex.RLock()
	defer managerMutex.RUnlock()

	if globalManager == nil {
		return nil
	}

	return globalManager.GetCustomMetric(name)
}

// UpdateSystemMetrics safely updates system resource metrics
func UpdateSystemMetrics(cpuUsage float64, memoryUsage float64, goroutineCount float64) {
	managerMutex.RLock()
	defer managerMutex.RUnlock()

	if globalManager == nil {
		return
	}

	globalManager.SystemCPUUsage.Set(cpuUsage)
	globalManager.SystemMemoryUsage.Set(memoryUsage)
	globalManager.GoroutineCount.Set(goroutineCount)
}

// StartSystemMetricsUpdater starts a goroutine to periodically update system metrics
func StartSystemMetricsUpdater(interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for range ticker.C {
			// Get current goroutine count
			goroutineCount := float64(runtime.NumGoroutine())

			// Get memory stats
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			memoryUsage := float64(m.Alloc)

			// For CPU usage, we'll use a simple approximation
			// In production, you might want to use a more sophisticated CPU monitoring library
			cpuUsage := 0.0 // This would need to be calculated from actual CPU usage

			UpdateSystemMetrics(cpuUsage, memoryUsage, goroutineCount)
		}
	}()
}

// CreateMetricsHandler creates an HTTP handler for metrics exposure
func CreateMetricsHandler() http.Handler {
	managerMutex.RLock()
	defer managerMutex.RUnlock()

	if globalManager == nil {
		// Return empty registry if no global manager
		registry := prometheus.NewRegistry()
		registry.MustRegister(
			collectors.NewGoCollector(),
			collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		)
		return promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
	}

	// Use the existing registry from the global manager
	return promhttp.HandlerFor(globalManager.promRegistry, promhttp.HandlerOpts{})
}
