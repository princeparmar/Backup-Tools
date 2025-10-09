package prometheus

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/push"
)

// PushGatewayClient handles pushing metrics to Prometheus push gateway
type PushGatewayClient struct {
	config   PushGatewayConfig
	pusher   *push.Pusher
	registry *prometheus.Registry
	stopChan chan struct{}
	mu       sync.RWMutex
}

// NewPushGatewayClient creates a new push gateway client
func NewPushGatewayClient(config PushGatewayConfig) *PushGatewayClient {
	registry := prometheus.NewRegistry()

	pusher := push.New(config.URL, config.JobName).
		Gatherer(registry).
		Grouping("instance", config.Instance)

	if config.Username != "" && config.Password != "" {
		pusher = pusher.BasicAuth(config.Username, config.Password)
	}

	return &PushGatewayClient{
		config:   config,
		pusher:   pusher,
		registry: registry,
		stopChan: make(chan struct{}),
	}
}

// RegisterMetrics registers metrics with the push gateway registry
func (pgc *PushGatewayClient) RegisterMetrics(metrics *Metrics) {
	if pgc == nil || metrics == nil {
		return
	}

	pgc.mu.Lock()
	defer pgc.mu.Unlock()

	// Register system metrics
	collectors := []prometheus.Collector{
		metrics.CPUUsage,
		metrics.MemoryUsage,
		metrics.GoroutineCount,
	}

	for _, collector := range collectors {
		if err := pgc.registry.Register(collector); err != nil {
			if _, ok := err.(*prometheus.AlreadyRegisteredError); !ok {
				logger.Warn(context.Background(), "Failed to register metric",
					logger.ErrorField(err))
			}
		}
	}

	// Register additional metrics from the metrics map
	metrics.mu.RLock()
	defer metrics.mu.RUnlock()
	for name, collector := range metrics.metrics {
		if err := pgc.registry.Register(collector); err != nil {
			if _, ok := err.(*prometheus.AlreadyRegisteredError); !ok {
				logger.Warn(context.Background(), "Failed to register metric",
					logger.String("metric", name),
					logger.ErrorField(err))
			}
		}
	}
}

// PushMetrics pushes metrics to the push gateway
func (pgc *PushGatewayClient) PushMetrics(ctx context.Context) error {
	if pgc == nil {
		return fmt.Errorf("push gateway client is nil")
	}

	httpClient := &http.Client{
		Timeout: 30 * time.Second,
	}

	if err := pgc.pusher.Client(httpClient).Push(); err != nil {
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
	if pgc == nil {
		return
	}

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
	if pgc == nil {
		return
	}

	select {
	case <-pgc.stopChan:
		// Already closed
	default:
		close(pgc.stopChan)
	}
}

// PushMetricsOnce pushes metrics once and returns
func (pgc *PushGatewayClient) PushMetricsOnce(ctx context.Context) error {
	if pgc == nil {
		return fmt.Errorf("push gateway client is nil")
	}
	return pgc.PushMetrics(ctx)
}

// StartPushGateway starts the push gateway with the given configuration
func StartPushGateway(ctx context.Context, config PushGatewayConfig) error {
	globalMu.Lock()
	defer globalMu.Unlock()

	if err := InitPushGateway(config); err != nil {
		return fmt.Errorf("initialize push gateway: %w", err)
	}

	if GlobalPushGateway != nil {
		go GlobalPushGateway.StartPeriodicPush(ctx)
	}
	return nil
}

// InitPushGateway initializes the global push gateway client
func InitPushGateway(config PushGatewayConfig) error {
	GlobalPushGateway = NewPushGatewayClient(config)

	globalMu.RLock()
	defer globalMu.RUnlock()
	if GlobalMetrics != nil {
		GlobalPushGateway.RegisterMetrics(GlobalMetrics)
	}

	return nil
}

// PushMetricsNow pushes metrics immediately using the global push gateway
func PushMetricsNow(ctx context.Context) error {
	globalMu.RLock()
	defer globalMu.RUnlock()

	if GlobalPushGateway == nil {
		return fmt.Errorf("push gateway not initialized")
	}
	return GlobalPushGateway.PushMetricsOnce(ctx)
}

// StopPushGateway stops the global push gateway
func StopPushGateway() {
	globalMu.Lock()
	defer globalMu.Unlock()

	if GlobalPushGateway != nil {
		GlobalPushGateway.Stop()
	}
}
