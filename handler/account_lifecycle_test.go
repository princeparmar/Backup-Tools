package handler_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/handler"
	"github.com/StorX2-0/Backup-Tools/middleware"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func TestAccountLifecycleHandlers_APIKeyAndValidation(t *testing.T) {
	prev := os.Getenv("BACKUP_TOOLS_API_KEY")
	t.Cleanup(func() { _ = os.Setenv("BACKUP_TOOLS_API_KEY", prev) })
	require.NoError(t, os.Setenv("BACKUP_TOOLS_API_KEY", "test-secret"))

	tests := []struct {
		name       string
		handler    echo.HandlerFunc
		apiKey     string
		body       map[string]interface{}
		wantStatus int
	}{
		{
			name:       "pending-delete missing api key",
			handler:    handler.HandleAccountPendingDelete,
			apiKey:     "",
			body:       map[string]interface{}{"satellite_user_id": "user-1"},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "pending-delete bad api key",
			handler:    handler.HandleAccountPendingDelete,
			apiKey:     "wrong",
			body:       map[string]interface{}{"satellite_user_id": "user-1"},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "pending-delete missing user id",
			handler:    handler.HandleAccountPendingDelete,
			apiKey:     "test-secret",
			body:       map[string]interface{}{},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "resume missing user id",
			handler:    handler.HandleAccountResume,
			apiKey:     "test-secret",
			body:       map[string]interface{}{},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "purge missing user id",
			handler:    handler.HandleAccountPurge,
			apiKey:     "test-secret",
			body:       map[string]interface{}{},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := echo.New()
			payload, err := json.Marshal(tt.body)
			require.NoError(t, err)
			req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			if tt.apiKey != "" {
				req.Header.Set("X-API-Key", tt.apiKey)
			}
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			c.Set(middleware.DbContextKey, &db.PostgresDb{})

			err = tt.handler(c)
			if he, ok := err.(*echo.HTTPError); ok {
				require.Equal(t, tt.wantStatus, he.Code)
				return
			}
			require.Equal(t, tt.wantStatus, rec.Code)
		})
	}
}
