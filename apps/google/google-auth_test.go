package google

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
)

// Mock database for testing
type mockDB struct{}

func (m *mockDB) WriteGoogleAuthToken(token, key string) {
	// Mock implementation
}

// Mock verifyToken function
func verifyTokenTest(_ string) (bool, error) {
	// Mock implementation
	return true, nil
}

func TestAutentificate(t *testing.T) {
	// Setup
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(""))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	// Mock database
	db := &mockDB{}
	c.Set(dbContextKey, db)

	// Test case: valid google key
	c.SetParamValues("google-key", "valid_token")
	err := Autentificate(c)

	// Assertions
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
	var response map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&response)
	assert.NotNil(t, response["google-auth"])

	// Test case: missing google key
	req = httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(""))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec = httptest.NewRecorder()
	c = e.NewContext(req, rec)
	c.Set(dbContextKey, db)
	err = Autentificate(c)

	// Assertions
	assert.Error(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestVerifyToken(t *testing.T) {
	// Test case: valid token
	valid, err := verifyTokenTest("valid_token")
	assert.True(t, valid)
	assert.NoError(t, err)

	// Test case: invalid token
	valid, err = verifyTokenTest("invalid_token")
	assert.False(t, valid)
	assert.NoError(t, err)
}
