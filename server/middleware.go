package server

import (
	"net/http"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
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
