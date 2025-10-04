package prometheus

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/logger"
)

type PushGatewayConfig struct {
	URL      string
	JobName  string
	Instance string
	Username string
	Password string
	Interval time.Duration
}

var (
	GlobalPushGateway *PushGatewayClient
	globalMu          sync.RWMutex
)

// InitFromEnv initializes Prometheus metrics and push gateway from environment variables
func InitFromEnv(ctx context.Context) error {
	// Initialize basic metrics
	InitMetrics()

	// Initialize push gateway if configured
	pushGatewayURL := os.Getenv("PROMETHEUS_PUSHGATEWAY_URL")
	if pushGatewayURL == "" {
		logger.Info(ctx, "No Prometheus push gateway URL configured, skipping push gateway setup")
		return nil
	}

	// Validate push gateway URL
	if _, err := url.Parse(pushGatewayURL); err != nil {
		logger.Error(ctx, "Invalid push gateway URL",
			logger.String("url", pushGatewayURL),
			logger.ErrorField(err))
		return fmt.Errorf("parse push gateway URL: %w", err)
	}

	config := PushGatewayConfig{
		URL:      pushGatewayURL,
		JobName:  getEnvWithDefault("PROMETHEUS_JOB_NAME", "backup-tools"),
		Instance: getEnvWithDefault("PROMETHEUS_INSTANCE", getHostname()),
		Username: os.Getenv("PROMETHEUS_USERNAME"),
		Password: os.Getenv("PROMETHEUS_PASSWORD"),
		Interval: getPushInterval(),
	}

	// Validate configuration
	if err := validatePushGatewayConfig(config); err != nil {
		logger.Error(ctx, "Invalid push gateway configuration", logger.ErrorField(err))
		return err
	}

	// Start push gateway
	if err := StartPushGateway(ctx, config); err != nil {
		logger.Error(ctx, "Failed to start Prometheus push gateway", logger.ErrorField(err))
		return err
	}

	logger.Info(ctx, "Prometheus push gateway started",
		logger.String("url", config.URL),
		logger.String("job", config.JobName),
		logger.String("instance", config.Instance),
		logger.String("interval", config.Interval.String()))

	return nil
}

// getEnvWithDefault gets environment variable with fallback to default value
func getEnvWithDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// getPushInterval parses push interval from environment with default fallback
func getPushInterval() time.Duration {
	intervalStr := os.Getenv("PROMETHEUS_PUSH_INTERVAL")
	if intervalStr == "" {
		return 30 * time.Second
	}

	interval, err := time.ParseDuration(intervalStr)
	if err != nil {
		logger.Warn(context.Background(), "Invalid push interval, using default",
			logger.String("interval", intervalStr),
			logger.ErrorField(err))
		return 30 * time.Second
	}

	// Validate minimum interval to prevent too frequent pushes
	if interval < 5*time.Second {
		logger.Warn(context.Background(), "Push interval too short, using minimum of 5s",
			logger.String("interval", intervalStr))
		return 5 * time.Second
	}

	return interval
}

// validatePushGatewayConfig validates the push gateway configuration
func validatePushGatewayConfig(config PushGatewayConfig) error {
	switch {
	case config.URL == "":
		return fmt.Errorf("push gateway URL is required")
	case config.JobName == "":
		return fmt.Errorf("job name is required")
	case config.Instance == "":
		return fmt.Errorf("instance is required")
	case config.Interval < 5*time.Second:
		return fmt.Errorf("push interval must be at least 5 seconds, got %v", config.Interval)
	}
	return nil
}

// getHostname returns the system hostname or "unknown" if unavailable
func getHostname() string {
	if hostname, err := os.Hostname(); err == nil {
		return hostname
	}
	return "unknown"
}
