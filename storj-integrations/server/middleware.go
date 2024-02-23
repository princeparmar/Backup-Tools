package server

import (
	"net/http"
	"storj-integrations/storage"

	"github.com/dgrijalva/jwt-go"
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
			return []byte("your-secret-key"), nil
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
