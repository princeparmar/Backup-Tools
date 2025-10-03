# Prometheus Metrics for Backup Tools

This document describes the Prometheus metrics implementation for the Backup Tools Satellite component.

## Overview

The Backup Tools application now includes comprehensive Prometheus metrics collection for monitoring:
- HTTP request metrics (duration, count, errors)
- Satellite operation metrics (upload/download duration, size, errors)
- System metrics (CPU, memory, goroutines)
- Bucket and object operation metrics

## Metrics Collected

### HTTP Request Metrics
- `satellite_request_duration_seconds` - Duration of HTTP requests
- `satellite_requests_total` - Total number of HTTP requests
- `satellite_request_errors_total` - Total number of failed requests

### Satellite Operation Metrics
- `satellite_upload_duration_seconds` - Duration of upload operations
- `satellite_download_duration_seconds` - Duration of download operations
- `satellite_upload_size_bytes` - Size of uploaded objects
- `satellite_download_size_bytes` - Size of downloaded objects
- `satellite_upload_errors_total` - Upload operation errors
- `satellite_download_errors_total` - Download operation errors

### System Metrics
- `satellite_cpu_usage_percent` - CPU usage percentage
- `satellite_memory_usage_bytes` - Memory usage in bytes
- `satellite_goroutines_total` - Number of goroutines

### Satellite-Specific Metrics
- `satellite_bucket_operations_total` - Bucket operations (create, ensure)
- `satellite_object_operations_total` - Object operations (upload, download, delete)
- `satellite_errors_total` - Satellite-related errors

## Configuration

### Environment Variables

Set the following environment variables to configure Prometheus metrics:

```bash
# Prometheus Push Gateway Configuration
PROMETHEUS_PUSHGATEWAY_URL=http://localhost:9091
PROMETHEUS_JOB_NAME=backup-tools
PROMETHEUS_INSTANCE=backup-tools-instance
PROMETHEUS_USERNAME=  # Optional: for authentication
PROMETHEUS_PASSWORD=  # Optional: for authentication
PROMETHEUS_PUSH_INTERVAL=30s  # Optional: default is 30s
```

### Using Pull Mode (Recommended for Production)

If you prefer to use Prometheus pull mode instead of push gateway:

1. Configure Prometheus to scrape the `/metrics` endpoint
2. Don't set `PROMETHEUS_PUSHGATEWAY_URL` environment variable
3. The metrics will be available at `http://localhost:8005/metrics`

## Setup Instructions

### Option 1: Using Docker Compose (Recommended)

1. Start the monitoring stack:
```bash
docker-compose -f docker-compose.monitoring.yml up -d
```

2. Configure your application with push gateway:
```bash
export PROMETHEUS_PUSHGATEWAY_URL=http://localhost:9091
export PROMETHEUS_JOB_NAME=backup-tools
export PROMETHEUS_INSTANCE=backup-tools-instance
```

3. Start your Backup Tools application

4. Access the services:
   - Prometheus: http://localhost:9090
   - Grafana: http://localhost:3000 (admin/admin)
   - Push Gateway: http://localhost:9091

### Option 2: Manual Setup

1. Install Prometheus and Grafana
2. Configure Prometheus with the provided `prometheus.yml`
3. Import the Grafana dashboard from `grafana-dashboard.json`
4. Configure your application environment variables

## Grafana Dashboard

A pre-configured Grafana dashboard is provided in `grafana-dashboard.json` that includes:

- Request rate and duration metrics
- Error rate monitoring
- Upload/download operation metrics
- System resource usage
- Bucket and object operation statistics
- Error tracking and analysis

### Dashboard Panels

1. **Request Rate** - Shows requests per second by method and endpoint
2. **Request Duration** - 95th and 50th percentile response times
3. **Error Rate** - Failed requests by error type
4. **Upload Operations** - Upload duration by bucket and status
5. **Download Operations** - Download duration by bucket and status
6. **Upload Size Distribution** - Size distribution of uploaded objects
7. **System Resources** - Memory usage and goroutine count
8. **Bucket Operations** - Bucket operation statistics
9. **Object Operations** - Object operation statistics
10. **Satellite Errors** - Error tracking by type and component

## Monitoring Best Practices

### Alerts

Consider setting up alerts for:
- High error rates (> 5% of requests)
- High response times (> 1 second for 95th percentile)
- High memory usage (> 80% of available memory)
- High goroutine count (> 1000)

### Example Alert Rules

```yaml
groups:
- name: backup-tools
  rules:
  - alert: HighErrorRate
    expr: rate(satellite_request_errors_total[5m]) > 0.05
    for: 2m
    labels:
      severity: warning
    annotations:
      summary: "High error rate detected"
      
  - alert: HighResponseTime
    expr: histogram_quantile(0.95, rate(satellite_request_duration_seconds_bucket[5m])) > 1
    for: 2m
    labels:
      severity: warning
    annotations:
      summary: "High response time detected"
```

## Troubleshooting

### Metrics Not Appearing

1. Check that the application is running with metrics enabled
2. Verify environment variables are set correctly
3. Check application logs for metrics-related errors
4. Ensure the push gateway is accessible (if using push mode)

### High Memory Usage

The metrics collection adds minimal overhead, but if you experience issues:
1. Increase the push interval: `PROMETHEUS_PUSH_INTERVAL=60s`
2. Consider using pull mode instead of push gateway
3. Monitor the `satellite_memory_usage_bytes` metric

### Push Gateway Issues

1. Check push gateway connectivity: `curl http://localhost:9091/metrics`
2. Verify authentication credentials if configured
3. Check push gateway logs for errors

## Development

### Adding New Metrics

To add new metrics:

1. Add the metric definition to `satellite/metrics.go`
2. Add recording calls in the relevant functions
3. Update the Grafana dashboard if needed
4. Test the metrics collection

### Testing Metrics

1. Start the application with metrics enabled
2. Make some requests to generate metrics
3. Check the `/metrics` endpoint: `curl http://localhost:8005/metrics`
4. Verify metrics appear in Prometheus/Grafana

## Security Considerations

- Use authentication for push gateway in production
- Consider using TLS for metrics endpoints
- Restrict access to monitoring infrastructure
- Regularly rotate credentials

## Performance Impact

The metrics collection adds minimal overhead:
- ~1-2% CPU usage increase
- ~1-5MB memory usage increase
- Network overhead depends on push interval and metric volume

For high-traffic applications, consider:
- Increasing push intervals
- Using pull mode instead of push gateway
- Filtering metrics to only essential ones

