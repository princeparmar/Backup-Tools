package middleware

import (
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	monkit "github.com/spacemonkeygo/monkit/v3"
)

var monkitRegistry = monkit.Default

// MonkitMiddleware records HTTP request metrics using Monkit.
// It captures essential metrics with controlled cardinality:
// - Request duration and count by method, path, and status class
// - Response sizes
// - Error counts
// All metrics are tagged with method, path, status class (not individual status codes).
func MonkitMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()

			// Get request details
			method := c.Request().Method
			path := sanitizePath(c.Request().URL.Path)

			// Execute the next handler
			err := next(c)

			// Calculate duration
			duration := time.Since(start)

			// Get response details
			statusCode := c.Response().Status
			responseSize := c.Response().Size

			// Create Monkit package for HTTP requests
			pkg := monkitRegistry.Package()

			// Create base tags with controlled cardinality
			baseTags := []monkit.SeriesTag{
				monkit.NewSeriesTag("method", method),
				monkit.NewSeriesTag("path", path),
				monkit.NewSeriesTag("status_class", getStatusClass(statusCode)),
			}

			// Record essential timing metrics (reduced cardinality)
			pkg.FloatVal("http_request_duration_seconds", baseTags...).Observe(duration.Seconds())

			// Record essential counter metrics (reduced cardinality)
			pkg.Counter("http_requests_total", baseTags...).Inc(1)

			// Record response size metrics
			pkg.FloatVal("http_response_size_bytes", baseTags...).Observe(float64(responseSize))

			// Record error metrics if there was an error (simplified)
			if err != nil {
				errorTags := append(baseTags, monkit.NewSeriesTag("error_type", getErrorType(err)))
				pkg.Counter("http_request_errors_total", errorTags...).Inc(1)
			}

			return err
		}
	}
}

// getStatusClass returns the HTTP status class (2xx, 3xx, 4xx, 5xx)
func getStatusClass(statusCode int) string {
	switch {
	case statusCode >= 200 && statusCode < 300:
		return "2xx"
	case statusCode >= 300 && statusCode < 400:
		return "3xx"
	case statusCode >= 400 && statusCode < 500:
		return "4xx"
	case statusCode >= 500:
		return "5xx"
	default:
		return "unknown"
	}
}

// getErrorType categorizes the error type
func getErrorType(err error) string {
	if err == nil {
		return "none"
	}

	// Check for common HTTP error types
	if httpErr, ok := err.(*echo.HTTPError); ok {
		switch httpErr.Code {
		case 400:
			return "bad_request"
		case 401:
			return "unauthorized"
		case 403:
			return "forbidden"
		case 404:
			return "not_found"
		case 500:
			return "internal_server_error"
		default:
			return "http_error"
		}
	}

	return "unknown_error"
}

// sanitizePath normalizes the path for consistent metrics
func sanitizePath(path string) string {
	if idx := strings.Index(path, "?"); idx != -1 {
		path = path[:idx]
	}
	if len(path) > 1 && strings.HasSuffix(path, "/") {
		path = strings.TrimSuffix(path, "/")
	}
	return replaceDynamicParams(path)
}

func replaceDynamicParams(path string) string {
	path = replaceFileExtensions(path)
	path = replaceIDs(path)
	return replaceNames(path)
}

func replaceFileExtensions(path string) string {
	extensions := []string{".jpg", ".jpeg", ".png", ".gif", ".pdf", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx", ".txt", ".zip", ".rar", ".mp4", ".mp3", ".avi", ".mov"}
	for _, ext := range extensions {
		if strings.Contains(path, ext) {
			if lastSlash := strings.LastIndex(path, "/"); lastSlash != -1 {
				filename := path[lastSlash+1:]
				if strings.Contains(filename, ext) {
					return path[:lastSlash+1] + "{filename}"
				}
			}
		}
	}
	return path
}

func replaceIDs(path string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if (len(part) == 36 && strings.Count(part, "-") == 4) || (len(part) > 0 && isNumeric(part)) {
			parts[i] = "{id}"
		}
	}
	return strings.Join(parts, "/")
}

func replaceNames(path string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if !isStaticRoutePart(part) && len(part) > 0 && !isNumeric(part) && !strings.Contains(part, ".") {
			parts[i] = "{name}"
		}
	}
	return strings.Join(parts, "/")
}

func isNumeric(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(s) > 0
}

func isStaticRoutePart(part string) bool {
	staticParts := map[string]bool{
		"google": true, "drive": true, "satellite": true, "folder": true, "list": true,
		"sync": true, "photos": true, "gmail": true, "storage": true, "cloud": true,
		"dropbox": true, "office365": true, "aws": true, "github": true, "shopify": true,
		"quickbooks": true, "auto-sync": true, "job": true, "task": true, "root": true,
		"shared": true, "bucket": true, "project": true, "organization": true,
		"album": true, "message": true, "customer": true, "product": true, "order": true,
		"item": true, "invoice": true, "repo": true, "repository": true,
	}
	return staticParts[part]
}
