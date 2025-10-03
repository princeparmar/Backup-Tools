package prometheus

import (
	"context"
	"os"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/logger"
)

// InitFromEnv initializes Prometheus metrics and push gateway from environment variables
func InitFromEnv(ctx context.Context) error {
	// Initialize basic metrics
	InitMetrics()

	// Initialize push gateway if configured
	pushGatewayURL := os.Getenv("PROMETHEUS_PUSHGATEWAY_URL")
	if pushGatewayURL == "" {
		return nil
	}

	config := PushGatewayConfig{
		URL:      pushGatewayURL,
		JobName:  getEnvWithDefault("PROMETHEUS_JOB_NAME", "backup-tools"),
		Instance: getEnvWithDefault("PROMETHEUS_INSTANCE", "backup-tools-instance"),
		Username: os.Getenv("PROMETHEUS_USERNAME"),
		Password: os.Getenv("PROMETHEUS_PASSWORD"),
		Interval: getPushInterval(),
	}

	// Start push gateway
	if err := StartPushGateway(ctx, config); err != nil {
		logger.Error(ctx, "Failed to start Prometheus push gateway", logger.ErrorField(err))
		return err
	}

	logger.Info(ctx, "Prometheus push gateway started",
		logger.String("url", config.URL),
		logger.String("job", config.JobName),
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

	return interval
}
