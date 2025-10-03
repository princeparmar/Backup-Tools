package server

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/prometheus"
	"github.com/StorX2-0/Backup-Tools/storage"

	"github.com/dgrijalva/jwt-go"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

const dbContextKey = "__db"

func DBMiddleware(db *storage.PosgresStore) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set(dbContextKey, db)
			return next(c)
		}
	}
}

func JWTMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		token := c.Request().Header.Get("Authorization")
		if token == "" {
			return echo.NewHTTPError(http.StatusUnauthorized, "missing JWT token")
		}

		jwtToken, err := jwt.Parse(token, func(token *jwt.Token) (interface{}, error) {
			// Check the token signing method
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, echo.NewHTTPError(http.StatusUnauthorized, "invalid token")
			}
			// Provide your JWT secret key here
			return []byte(google.JwtSecretKey), nil
		})

		if err != nil {
			return echo.NewHTTPError(http.StatusUnauthorized, err.Error())
		}

		if jwtToken.Valid {
			// Token is valid, proceed with the next middleware
			return next(c)
		}

		return echo.NewHTTPError(http.StatusUnauthorized, "invalid token")
	}
}

// Alternative version with defer for completion log
func TraceIDMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) (err error) { // Named return value
			// Generate a unique trace ID for this request
			traceID := uuid.New().String()

			// Store trace ID in Echo context
			c.Set("trace_id", traceID)

			// Add trace ID to the request context
			ctx := logger.WithTraceID(c.Request().Context(), traceID)
			c.SetRequest(c.Request().WithContext(ctx))

			// Log the start of the request with trace ID
			logger.Info(ctx, "Request started",
				logger.String("method", c.Request().Method),
				logger.String("path", c.Request().URL.Path),
				logger.String("remote_addr", c.Request().RemoteAddr),
				logger.String("user_agent", c.Request().UserAgent()),
			)

			start := time.Now()

			defer func() {
				duration := time.Since(start)
				status := c.Response().Status

				fields := []logger.Field{
					logger.String("method", c.Request().Method),
					logger.String("path", c.Request().URL.Path),
					logger.Int("status", status),
					logger.Int("response_size", int(c.Response().Size)),
					logger.String("duration", duration.String()),
				}

				// If there was an error, log it as error level
				if err != nil {
					logger.Error(ctx, "Request failed", append(fields, logger.ErrorField(err))...)
				} else {
					logger.Info(ctx, "Request completed", fields...)
				}
			}()

			// Process the request - error will be captured by named return value
			err = next(c)
			return err
		}
	}
}

func PrometheusMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()

			// Optional: panic recovery
			defer func() {
				if r := recover(); r != nil {
					prometheus.RecordRequestError(c.Request().Method, c.Request().URL.Path, "panic")
					panic(r)
				}
			}()

			err := next(c)
			duration := time.Since(start)

			method := c.Request().Method
			path := sanitizePath(c.Request().URL.Path) // Sanitize path
			status := strconv.Itoa(c.Response().Status)

			prometheus.RecordRequestDuration(method, path, status, duration)
			prometheus.RecordRequestTotal(method, path, status)

			if err != nil {
				prometheus.RecordRequestError(method, path, "handler_error")
				// Note: Echo might handle the error, we're just recording it
			}

			return err
		}
	}
}

func sanitizePath(path string) string {
	// Remove query parameters
	if idx := strings.Index(path, "?"); idx != -1 {
		path = path[:idx]
	}

	// Remove trailing slashes except for root
	if len(path) > 1 && strings.HasSuffix(path, "/") {
		path = strings.TrimSuffix(path, "/")
	}

	// Replace dynamic path parameters with placeholders
	path = replaceDynamicParams(path)

	return path
}

func replaceDynamicParams(path string) string {
	// Handle file extensions first (most specific)
	path = replaceFileExtensions(path)

	// Handle IDs
	path = replaceIDs(path)

	// Handle names
	path = replaceNames(path)

	return path
}

func replaceFileExtensions(path string) string {
	// Replace file extensions with placeholder
	extensions := []string{".jpg", ".jpeg", ".png", ".gif", ".pdf", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx", ".txt", ".zip", ".rar", ".mp4", ".mp3", ".avi", ".mov"}
	for _, ext := range extensions {
		if strings.Contains(path, ext) {
			// Find the last occurrence and replace the filename part
			lastSlash := strings.LastIndex(path, "/")
			if lastSlash != -1 {
				filename := path[lastSlash+1:]
				if strings.Contains(filename, ext) {
					path = path[:lastSlash+1] + "{filename}"
					break
				}
			}
		}
	}
	return path
}

func replaceIDs(path string) string {
	// Replace UUIDs and numeric IDs
	parts := strings.Split(path, "/")
	for i, part := range parts {
		// Check if it's a UUID (8-4-4-4-12 pattern)
		if len(part) == 36 && strings.Count(part, "-") == 4 {
			parts[i] = "{id}"
		} else if len(part) > 0 && isNumeric(part) {
			parts[i] = "{id}"
		}
	}
	return strings.Join(parts, "/")
}

func replaceNames(path string) string {
	// Replace dynamic names in common route patterns
	parts := strings.Split(path, "/")
	for i, part := range parts {
		// Skip known static parts
		if isStaticRoutePart(part) {
			continue
		}
		// Replace if it looks like a dynamic name
		if len(part) > 0 && !isNumeric(part) && !strings.Contains(part, ".") {
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
