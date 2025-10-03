package prometheus

import (
	"context"
	"runtime"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics holds all Prometheus metrics for the Satellite component
type Metrics struct {
	// Request metrics
	RequestDuration *prometheus.HistogramVec
	RequestTotal    *prometheus.CounterVec
	RequestErrors   *prometheus.CounterVec

	// Operation metrics
	UploadDuration   *prometheus.HistogramVec
	DownloadDuration *prometheus.HistogramVec
	UploadSize       *prometheus.HistogramVec
	DownloadSize     *prometheus.HistogramVec
	UploadErrors     *prometheus.CounterVec
	DownloadErrors   *prometheus.CounterVec

	// System metrics
	CPUUsage       prometheus.Gauge
	MemoryUsage    prometheus.Gauge
	GoroutineCount prometheus.Gauge

	// Satellite-specific metrics
	BucketOperations *prometheus.CounterVec
	ObjectOperations *prometheus.CounterVec
	SatelliteErrors  *prometheus.CounterVec
}

// NewMetrics creates a new Metrics instance
func NewMetrics() *Metrics {
	buckets := prometheus.ExponentialBuckets(1024, 2, 20) // 1KB to 1GB

	return &Metrics{
		RequestDuration: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "satellite_request_duration_seconds",
			Help:    "Duration of HTTP requests to satellite endpoints",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "endpoint", "status"}),

		RequestTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "satellite_requests_total",
			Help: "Total number of HTTP requests to satellite endpoints",
		}, []string{"method", "endpoint", "status"}),

		RequestErrors: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "satellite_request_errors_total",
			Help: "Total number of failed HTTP requests to satellite endpoints",
		}, []string{"method", "endpoint", "error_type"}),

		UploadDuration: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "satellite_upload_duration_seconds",
			Help:    "Duration of upload operations to satellite",
			Buckets: prometheus.DefBuckets,
		}, []string{"bucket", "status"}),

		DownloadDuration: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "satellite_download_duration_seconds",
			Help:    "Duration of download operations from satellite",
			Buckets: prometheus.DefBuckets,
		}, []string{"bucket", "status"}),

		UploadSize: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "satellite_upload_size_bytes",
			Help:    "Size of uploaded objects to satellite",
			Buckets: buckets,
		}, []string{"bucket"}),

		DownloadSize: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "satellite_download_size_bytes",
			Help:    "Size of downloaded objects from satellite",
			Buckets: buckets,
		}, []string{"bucket"}),

		UploadErrors: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "satellite_upload_errors_total",
			Help: "Total number of upload errors to satellite",
		}, []string{"bucket", "error_type"}),

		DownloadErrors: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "satellite_download_errors_total",
			Help: "Total number of download errors from satellite",
		}, []string{"bucket", "error_type"}),

		CPUUsage: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "satellite_cpu_usage_percent",
			Help: "Current CPU usage percentage",
		}),

		MemoryUsage: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "satellite_memory_usage_bytes",
			Help: "Current memory usage in bytes",
		}),

		GoroutineCount: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "satellite_goroutines_total",
			Help: "Current number of goroutines",
		}),

		BucketOperations: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "satellite_bucket_operations_total",
			Help: "Total number of bucket operations",
		}, []string{"bucket", "operation", "status"}),

		ObjectOperations: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "satellite_object_operations_total",
			Help: "Total number of object operations",
		}, []string{"bucket", "operation", "status"}),

		SatelliteErrors: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "satellite_errors_total",
			Help: "Total number of satellite-related errors",
		}, []string{"error_type", "component"}),
	}
}

// Global metrics instance
var GlobalMetrics *Metrics

// InitMetrics initializes the global metrics instance
func InitMetrics() {
	if GlobalMetrics == nil {
		GlobalMetrics = NewMetrics()
	}
}

// Metric recording methods
func (m *Metrics) RecordRequestDuration(method, endpoint, status string, duration time.Duration) {
	m.RequestDuration.WithLabelValues(method, endpoint, status).Observe(duration.Seconds())
}

func (m *Metrics) RecordRequestTotal(method, endpoint, status string) {
	m.RequestTotal.WithLabelValues(method, endpoint, status).Inc()
}

func (m *Metrics) RecordRequestError(method, endpoint, errorType string) {
	m.RequestErrors.WithLabelValues(method, endpoint, errorType).Inc()
}

func (m *Metrics) RecordUploadDuration(bucket, status string, duration time.Duration) {
	m.UploadDuration.WithLabelValues(bucket, status).Observe(duration.Seconds())
}

func (m *Metrics) RecordDownloadDuration(bucket, status string, duration time.Duration) {
	m.DownloadDuration.WithLabelValues(bucket, status).Observe(duration.Seconds())
}

func (m *Metrics) RecordUploadSize(bucket string, size int64) {
	m.UploadSize.WithLabelValues(bucket).Observe(float64(size))
}

func (m *Metrics) RecordDownloadSize(bucket string, size int64) {
	m.DownloadSize.WithLabelValues(bucket).Observe(float64(size))
}

func (m *Metrics) RecordUploadError(bucket, errorType string) {
	m.UploadErrors.WithLabelValues(bucket, errorType).Inc()
}

func (m *Metrics) RecordDownloadError(bucket, errorType string) {
	m.DownloadErrors.WithLabelValues(bucket, errorType).Inc()
}

func (m *Metrics) RecordBucketOperation(bucket, operation, status string) {
	m.BucketOperations.WithLabelValues(bucket, operation, status).Inc()
}

func (m *Metrics) RecordObjectOperation(bucket, operation, status string) {
	m.ObjectOperations.WithLabelValues(bucket, operation, status).Inc()
}

func (m *Metrics) RecordSatelliteError(errorType, component string) {
	m.SatelliteErrors.WithLabelValues(errorType, component).Inc()
}

// UpdateSystemMetrics updates system-level metrics
func (m *Metrics) UpdateSystemMetrics() {
	m.GoroutineCount.Set(float64(runtime.NumGoroutine()))

	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	m.MemoryUsage.Set(float64(memStats.Alloc))

	// CPU usage would require more complex implementation
	m.CPUUsage.Set(0)
}

// StartSystemMetricsCollection starts periodic system metrics collection
func (m *Metrics) StartSystemMetricsCollection(ctx context.Context) {
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
func RecordRequestDuration(method, endpoint, status string, duration time.Duration) {
	if GlobalMetrics != nil {
		GlobalMetrics.RecordRequestDuration(method, endpoint, status, duration)
	}
}

func RecordRequestTotal(method, endpoint, status string) {
	if GlobalMetrics != nil {
		GlobalMetrics.RecordRequestTotal(method, endpoint, status)
	}
}

func RecordRequestError(method, endpoint, errorType string) {
	if GlobalMetrics != nil {
		GlobalMetrics.RecordRequestError(method, endpoint, errorType)
	}
}

func RecordUploadDuration(bucket, status string, duration time.Duration) {
	if GlobalMetrics != nil {
		GlobalMetrics.RecordUploadDuration(bucket, status, duration)
	}
}

func RecordDownloadDuration(bucket, status string, duration time.Duration) {
	if GlobalMetrics != nil {
		GlobalMetrics.RecordDownloadDuration(bucket, status, duration)
	}
}

func RecordUploadSize(bucket string, size int64) {
	if GlobalMetrics != nil {
		GlobalMetrics.RecordUploadSize(bucket, size)
	}
}

func RecordDownloadSize(bucket string, size int64) {
	if GlobalMetrics != nil {
		GlobalMetrics.RecordDownloadSize(bucket, size)
	}
}

func RecordUploadError(bucket, errorType string) {
	if GlobalMetrics != nil {
		GlobalMetrics.RecordUploadError(bucket, errorType)
	}
}

func RecordDownloadError(bucket, errorType string) {
	if GlobalMetrics != nil {
		GlobalMetrics.RecordDownloadError(bucket, errorType)
	}
}

func RecordBucketOperation(bucket, operation, status string) {
	if GlobalMetrics != nil {
		GlobalMetrics.RecordBucketOperation(bucket, operation, status)
	}
}

func RecordObjectOperation(bucket, operation, status string) {
	if GlobalMetrics != nil {
		GlobalMetrics.RecordObjectOperation(bucket, operation, status)
	}
}

func RecordSatelliteError(errorType, component string) {
	if GlobalMetrics != nil {
		GlobalMetrics.RecordSatelliteError(errorType, component)
	}
}
