package server

import (
	"storj-integrations/storage"

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
