package middleware

import (
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/prometheus"
	"github.com/StorX2-0/Backup-Tools/storage"

	"github.com/dgrijalva/jwt-go"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	echomiddleware "github.com/labstack/echo/v4/middleware"
)

const DbContextKey = "__db"

var (
	JwtSecretKey    = "your-secret-key"
	TokenExpiration = 24 * time.Hour
)

// InitializeAllMiddleware sets up all middleware for the Echo server
func InitializeAllMiddleware(e *echo.Echo, db *storage.PosgresStore) {
	if os.Getenv("HTTP_LOGGING") == "true" {
		e.Use(echomiddleware.Logger())
	}
	e.Use(echomiddleware.Recover())
	e.Use(TraceIDMiddleware())
	e.Use(PrometheusMiddleware())
	e.Use(DBMiddleware(db))
	e.Use(echomiddleware.CORS())
}

func DBMiddleware(db *storage.PosgresStore) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set(DbContextKey, db)
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
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, echo.NewHTTPError(http.StatusUnauthorized, "invalid token")
			}
			return []byte(JwtSecretKey), nil
		})

		if err != nil || !jwtToken.Valid {
			return echo.NewHTTPError(http.StatusUnauthorized, "invalid token")
		}

		return next(c)
	}
}

func TraceIDMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) (err error) {
			traceID := uuid.New().String()
			c.Set("trace_id", traceID)

			ctx := logger.WithTraceID(c.Request().Context(), traceID)
			c.SetRequest(c.Request().WithContext(ctx))

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
				if err != nil {
					logger.Error(ctx, "Request failed", append(fields, logger.ErrorField(err))...)
				} else {
					logger.Info(ctx, "Request completed", fields...)
				}
			}()

			return next(c)
		}
	}
}

func PrometheusMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()
			err := next(c)
			duration := time.Since(start)

			method := c.Request().Method
			path := sanitizePath(c.Request().URL.Path)
			status := c.Response().Status

			prometheus.RecordTimer("request_duration_seconds", duration, "method", method, "path", path)
			prometheus.RecordCounter("requests_total", 1, "method", method, "path", path, "status", strconv.Itoa(status))

			if err != nil {
				prometheus.RecordError("handler_error", "middleware")
			}

			return err
		}
	}
}

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
