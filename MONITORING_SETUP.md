# Backup Tools - Comprehensive Monitoring Setup Guide

This guide provides complete instructions for setting up comprehensive monitoring for the Backup Tools application using Prometheus, Grafana, and custom alerting.

## Overview

The monitoring setup includes:
- **Prometheus** - Metrics collection and alerting
- **Grafana** - Visualization and dashboards
- **Push Gateway** - Metrics aggregation
- **Custom Dashboards** - Application-specific monitoring
- **Alert Rules** - Proactive issue detection

## Quick Start

### 1. Start Monitoring Stack

```bash
# Start all monitoring services
docker-compose -f docker-compose.monitoring.yml up -d

# Verify services are running
docker ps
```

### 2. Start Backup Tools Application

```bash
# Set environment variables
export PROMETHEUS_PUSHGATEWAY_URL=http://localhost:9091
export PROMETHEUS_JOB_NAME=backup-tools
export PROMETHEUS_INSTANCE=backup-tools-instance

# Start the application
source .env && go run cmd/main.go
```

### 3. Access Monitoring Interfaces

- **Prometheus**: http://localhost:9090
- **Grafana**: http://localhost:3000 (admin/admin)
- **Push Gateway**: http://localhost:9091
- **Backup Tools**: http://localhost:8005

## Dashboard Setup

### 1. Import Main Monitoring Dashboard

1. Open Grafana at http://localhost:3000
2. Go to **Dashboards** → **Import**
3. Copy the contents of `grafana-dashboard-comprehensive.json`
4. Paste into the import dialog
5. Click **Import**

### 2. Import Alerts Dashboard

1. Go to **Dashboards** → **Import**
2. Copy the contents of `grafana-alerts-dashboard.json`
3. Paste into the import dialog
4. Click **Import**

## Alert Configuration

### 1. Prometheus Alert Rules

The alert rules are automatically loaded from `prometheus-alerts.yml`. Key alerts include:

#### Critical Alerts
- **BackupToolsDown**: Application is down
- **VeryHighErrorRate**: Error rate > 10%
- **VeryHighResponseTime**: Response time > 5s
- **VeryHighMemoryUsage**: Memory > 500MB
- **VeryHighGoroutineCount**: Goroutines > 5000

#### Warning Alerts
- **HighErrorRate**: Error rate > 5%
- **HighResponseTime**: Response time > 1s
- **HighMemoryUsage**: Memory > 100MB
- **HighGoroutineCount**: Goroutines > 1000
- **HighSatelliteUploadErrors**: Upload errors > 0.1/s
- **HighSatelliteDownloadErrors**: Download errors > 0.1/s

### 2. Alert Notification Setup

To configure alert notifications (email, Slack, etc.):

1. Go to **Alerting** → **Notification channels** in Grafana
2. Add your notification channel
3. Create alert rules that use the channel

## Dashboard Panels Explained

### Main Monitoring Dashboard

#### Overview Section
- **Application Status**: Shows if services are UP/DOWN
- **Request Rate**: HTTP requests per second
- **Response Time**: 95th percentile response time
- **Error Rate**: Percentage of failed requests
- **Goroutines**: Active goroutine count

#### Performance Section
- **Request Rate Over Time**: HTTP request trends
- **Response Time Distribution**: 50th, 95th, 99th percentiles
- **Memory Usage**: Heap allocation and system memory
- **Garbage Collection**: GC duration and frequency

#### Operations Section
- **HTTP Status Codes**: Request distribution by status
- **System Resources**: Goroutines, allocations, frees
- **Satellite Operations**: Upload/download duration and size
- **Error Tracking**: Request, upload, download errors

#### Data Tables
- **Bucket Operations**: Bucket operation statistics
- **Object Operations**: Object operation statistics
- **Satellite Errors**: Error breakdown by component

### Alerts Dashboard

#### Alert Overview
- **Current Alerts**: Firing and pending alerts count
- **Severity Distribution**: Pie chart of alert severities
- **Alert History**: Timeline of alert changes
- **Health Score**: Overall service health percentage

#### Alert Tables
- **Critical Alerts**: Currently firing critical alerts
- **Warning Alerts**: Currently firing warning alerts
- **Alert Frequency**: Alerts by service component

## Custom Metrics

The application exposes both standard Go metrics and custom satellite metrics:

### Standard Go Metrics
- `go_goroutines` - Number of goroutines
- `go_memstats_*` - Memory statistics
- `go_gc_*` - Garbage collection metrics
- `promhttp_metric_handler_*` - HTTP handler metrics

### Custom Satellite Metrics
- `satellite_request_duration_seconds` - Request duration
- `satellite_requests_total` - Request count
- `satellite_request_errors_total` - Request errors
- `satellite_upload_duration_seconds` - Upload duration
- `satellite_download_duration_seconds` - Download duration
- `satellite_upload_size_bytes` - Upload size
- `satellite_download_size_bytes` - Download size
- `satellite_upload_errors_total` - Upload errors
- `satellite_download_errors_total` - Download errors
- `satellite_bucket_operations_total` - Bucket operations
- `satellite_object_operations_total` - Object operations
- `satellite_errors_total` - Satellite errors

## Troubleshooting

### Common Issues

#### 1. Metrics Not Appearing
```bash
# Check if application is running
curl http://localhost:8005/metrics

# Check if push gateway is running
curl http://localhost:9091/metrics

# Check Prometheus targets
# Go to http://localhost:9090/targets
```

#### 2. Alerts Not Firing
```bash
# Check alert rules are loaded
# Go to http://localhost:9090/rules

# Check alert expression
# Go to http://localhost:9090/graph
# Enter: ALERTS{alertstate="firing"}
```

#### 3. Dashboard Not Loading
```bash
# Check Prometheus data source
# Go to Grafana → Configuration → Data Sources
# Verify Prometheus URL: http://prometheus:9090

# Check dashboard queries
# Edit dashboard panel and verify query syntax
```

### Performance Tuning

#### 1. Reduce Memory Usage
```bash
# Increase push interval
export PROMETHEUS_PUSH_INTERVAL=60s

# Use pull mode instead of push gateway
unset PROMETHEUS_PUSHGATEWAY_URL
```

#### 2. Reduce Alert Noise
```yaml
# Increase alert thresholds in prometheus-alerts.yml
# Example: Change error rate from 5% to 10%
expr: rate(promhttp_metric_handler_requests_total{code!="200"}[5m]) / rate(promhttp_metric_handler_requests_total[5m]) * 100 > 10
```

## Security Considerations

### 1. Authentication
- Change default Grafana admin password
- Use strong passwords for all services
- Enable HTTPS in production

### 2. Network Security
- Restrict access to monitoring ports
- Use firewall rules
- Consider VPN access for sensitive environments

### 3. Data Retention
- Configure appropriate retention periods
- Monitor disk usage
- Set up log rotation

## Production Deployment

### 1. High Availability
- Deploy Prometheus in HA mode
- Use external storage for Grafana
- Set up backup procedures

### 2. Scaling
- Use Prometheus federation for multiple instances
- Consider Thanos for long-term storage
- Implement proper load balancing

### 3. Monitoring the Monitors
- Set up external monitoring
- Use health checks
- Implement circuit breakers

## Maintenance

### 1. Regular Tasks
- Review alert thresholds monthly
- Update dashboard queries as needed
- Clean up old metrics and logs

### 2. Backup
- Export dashboard configurations
- Backup Prometheus data
- Document custom configurations

### 3. Updates
- Keep Prometheus and Grafana updated
- Review and update alert rules
- Test monitoring after application updates

## Support

For issues with monitoring setup:
1. Check the troubleshooting section
2. Review application logs
3. Verify service connectivity
4. Check Prometheus and Grafana logs

## Additional Resources

- [Prometheus Documentation](https://prometheus.io/docs/)
- [Grafana Documentation](https://grafana.com/docs/)
- [Go Prometheus Client](https://github.com/prometheus/client_golang)
- [Alerting Best Practices](https://prometheus.io/docs/practices/alerting/)
