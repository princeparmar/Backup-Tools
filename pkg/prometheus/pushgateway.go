package prometheus

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/push"
)

// PushGatewayConfig holds configuration for the Prometheus push gateway
type PushGatewayConfig struct {
	URL      string
	JobName  string
	Instance string
	Username string
	Password string
	Interval time.Duration
}

// PushGatewayClient handles pushing metrics to Prometheus push gateway
type PushGatewayClient struct {
	config     PushGatewayConfig
	pusher     *push.Pusher
	registry   *prometheus.Registry
	httpClient *http.Client
	stopChan   chan struct{}
}

// NewPushGatewayClient creates a new push gateway client
func NewPushGatewayClient(config PushGatewayConfig) *PushGatewayClient {
	registry := prometheus.NewRegistry()

	httpClient := &http.Client{
		Timeout: 30 * time.Second,
	}

	pusher := push.New(config.URL, config.JobName).Gatherer(registry)

	if config.Username != "" && config.Password != "" {
		pusher = pusher.BasicAuth(config.Username, config.Password)
	}

	if config.Instance != "" {
		pusher = pusher.Grouping("instance", config.Instance)
	}

	return &PushGatewayClient{
		config:     config,
		pusher:     pusher,
		registry:   registry,
		httpClient: httpClient,
		stopChan:   make(chan struct{}),
	}
}

// RegisterMetrics registers metrics with the push gateway registry
func (pgc *PushGatewayClient) RegisterMetrics(metrics *Metrics) {
	collectors := []prometheus.Collector{
		metrics.RequestDuration,
		metrics.RequestTotal,
		metrics.RequestErrors,
		metrics.UploadDuration,
		metrics.DownloadDuration,
		metrics.UploadSize,
		metrics.DownloadSize,
		metrics.UploadErrors,
		metrics.DownloadErrors,
		metrics.CPUUsage,
		metrics.MemoryUsage,
		metrics.GoroutineCount,
		metrics.BucketOperations,
		metrics.ObjectOperations,
		metrics.SatelliteErrors,
		// Cron job metrics
		metrics.CronJobExecutions,
		metrics.CronJobDuration,
		metrics.CronJobErrors,
		metrics.CronJobRetries,
		metrics.TaskCreations,
		metrics.TaskCompletions,
		metrics.TaskFailures,
		metrics.HeartbeatMisses,
		metrics.JobDeactivations,
	}

	for _, collector := range collectors {
		if err := pgc.registry.Register(collector); err != nil {
			logger.Warn(context.Background(), "Failed to register metric",
				logger.ErrorField(err))
		}
	}
}

// PushMetrics pushes metrics to the push gateway
func (pgc *PushGatewayClient) PushMetrics(ctx context.Context) error {
	if err := pgc.pusher.Client(pgc.httpClient).Push(); err != nil {
		logger.Error(ctx, "Failed to push metrics to push gateway",
			logger.String("url", pgc.config.URL),
			logger.String("job", pgc.config.JobName),
			logger.ErrorField(err))
		return fmt.Errorf("push metrics: %w", err)
	}

	logger.Debug(ctx, "Successfully pushed metrics",
		logger.String("url", pgc.config.URL),
		logger.String("job", pgc.config.JobName))

	return nil
}

// StartPeriodicPush starts a goroutine that periodically pushes metrics
func (pgc *PushGatewayClient) StartPeriodicPush(ctx context.Context) {
	interval := pgc.config.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	logger.Info(ctx, "Starting periodic metrics push",
		logger.String("url", pgc.config.URL),
		logger.String("job", pgc.config.JobName),
		logger.String("interval", interval.String()))

	for {
		select {
		case <-ctx.Done():
			logger.Info(ctx, "Stopping periodic metrics push due to context cancellation")
			return
		case <-pgc.stopChan:
			logger.Info(ctx, "Stopping periodic metrics push")
			return
		case <-ticker.C:
			if err := pgc.PushMetrics(ctx); err != nil {
				logger.Error(ctx, "Periodic metrics push failed", logger.ErrorField(err))
			}
		}
	}
}

// Stop stops the periodic push
func (pgc *PushGatewayClient) Stop() {
	select {
	case <-pgc.stopChan:
		// Already closed
	default:
		close(pgc.stopChan)
	}
}

// PushMetricsOnce pushes metrics once and returns
func (pgc *PushGatewayClient) PushMetricsOnce(ctx context.Context) error {
	return pgc.PushMetrics(ctx)
}

// Global push gateway client
var GlobalPushGateway *PushGatewayClient

// InitPushGateway initializes the global push gateway client
func InitPushGateway(config PushGatewayConfig) error {
	GlobalPushGateway = NewPushGatewayClient(config)

	if GlobalMetrics != nil {
		GlobalPushGateway.RegisterMetrics(GlobalMetrics)
	}

	return nil
}

// StartPushGateway starts the push gateway with the given configuration
func StartPushGateway(ctx context.Context, config PushGatewayConfig) error {
	if err := InitPushGateway(config); err != nil {
		return fmt.Errorf("initialize push gateway: %w", err)
	}

	go GlobalPushGateway.StartPeriodicPush(ctx)
	return nil
}

// PushMetricsNow pushes metrics immediately using the global push gateway
func PushMetricsNow(ctx context.Context) error {
	if GlobalPushGateway == nil {
		return fmt.Errorf("push gateway not initialized")
	}
	return GlobalPushGateway.PushMetricsOnce(ctx)
}

// StopPushGateway stops the global push gateway
func StopPushGateway() {
	if GlobalPushGateway != nil {
		GlobalPushGateway.Stop()
	}
}
