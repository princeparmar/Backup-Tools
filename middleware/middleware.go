package middleware

import (
	"net/http"
	"time"

	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"

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
func InitializeAllMiddleware(e *echo.Echo, db *db.PosgresStore) {
	if utils.GetEnvWithKey("HTTP_LOGGING") == "true" {
		e.Use(echomiddleware.Logger())
	}
	e.Use(echomiddleware.Recover())
	e.Use(TraceIDMiddleware())
	e.Use(MonkitMiddleware())
	e.Use(DBMiddleware(db))
	e.Use(echomiddleware.CORS())
}

func DBMiddleware(db *db.PosgresStore) echo.MiddlewareFunc {
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
