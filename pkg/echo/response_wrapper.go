package echo

import (
	"io"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
)

// Response is the standard API response structure
type Response struct {
	Success bool        `json:"success"`
	Message string      `json:"message,omitempty"`
	Data    interface{} `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
	Meta    interface{} `json:"meta,omitempty"`
}

// PaginatedResponse for paginated data
type PaginatedResponse struct {
	Response
	Pagination *Pagination `json:"pagination,omitempty"`
}

// Pagination metadata
type Pagination struct {
	Page       int `json:"page"`
	PerPage    int `json:"per_page"`
	TotalCount int `json:"total_count"`
	TotalPages int `json:"total_pages"`
}

// ==================== SUCCESS RESPONSES ====================

// OK - 200
func OK(c echo.Context, message string, data interface{}) error {
	return c.JSON(http.StatusOK, Response{
		Success: true,
		Message: message,
		Data:    data,
	})
}

// Created - 201
func Created(c echo.Context, message string, data interface{}) error {
	return c.JSON(http.StatusCreated, Response{
		Success: true,
		Message: message,
		Data:    data,
	})
}

// Accepted - 202
func Accepted(c echo.Context, message string, data interface{}) error {
	return c.JSON(http.StatusAccepted, Response{
		Success: true,
		Message: message,
		Data:    data,
	})
}

// NoContent - 204
func NoContent(c echo.Context) error {
	return c.NoContent(http.StatusNoContent)
}

// ==================== CLIENT ERROR RESPONSES ====================

// BadRequest - 400
func BadRequest(c echo.Context, message string, err error) error {
	errorMsg := message
	if err != nil {
		errorMsg = err.Error()
	}

	return c.JSON(http.StatusBadRequest, Response{
		Success: false,
		Error:   errorMsg,
	})
}

// Unauthorized - 401
func Unauthorized(c echo.Context, message string, err error) error {
	errorMsg := message
	if err != nil {
		errorMsg = err.Error()
	}

	return c.JSON(http.StatusUnauthorized, Response{
		Success: false,
		Error:   errorMsg,
	})
}

// Forbidden - 403
func Forbidden(c echo.Context, message string, err error) error {
	errorMsg := message
	if err != nil {
		errorMsg = err.Error()
	}

	return c.JSON(http.StatusForbidden, Response{
		Success: false,
		Error:   errorMsg,
	})
}

// NotFound - 404
func NotFound(c echo.Context, message string, err error) error {
	errorMsg := message
	if err != nil {
		errorMsg = err.Error()
	}

	return c.JSON(http.StatusNotFound, Response{
		Success: false,
		Error:   errorMsg,
	})
}

// MethodNotAllowed - 405
func MethodNotAllowed(c echo.Context, message string, err error) error {
	errorMsg := message
	if err != nil {
		errorMsg = err.Error()
	}

	return c.JSON(http.StatusMethodNotAllowed, Response{
		Success: false,
		Error:   errorMsg,
	})
}

// Conflict - 409
func Conflict(c echo.Context, message string, err error) error {
	errorMsg := message
	if err != nil {
		errorMsg = err.Error()
	}

	return c.JSON(http.StatusConflict, Response{
		Success: false,
		Error:   errorMsg,
	})
}

// UnprocessableEntity - 422
func UnprocessableEntity(c echo.Context, message string, err error) error {
	errorMsg := message
	if err != nil {
		errorMsg = err.Error()
	}

	return c.JSON(http.StatusUnprocessableEntity, Response{
		Success: false,
		Error:   errorMsg,
	})
}

// TooManyRequests - 429
func TooManyRequests(c echo.Context, message string, err error) error {
	errorMsg := message
	if err != nil {
		errorMsg = err.Error()
	}

	return c.JSON(http.StatusTooManyRequests, Response{
		Success: false,
		Error:   errorMsg,
	})
}

// ==================== SERVER ERROR RESPONSES ====================

// InternalServerError - 500
func InternalServerError(c echo.Context, message string, err error) error {
	errorMsg := message
	if err != nil {
		errorMsg = err.Error()
	}

	return c.JSON(http.StatusInternalServerError, Response{
		Success: false,
		Error:   errorMsg,
	})
}

// NotImplemented - 501
func NotImplemented(c echo.Context, message string, err error) error {
	errorMsg := message
	if err != nil {
		errorMsg = err.Error()
	}

	return c.JSON(http.StatusNotImplemented, Response{
		Success: false,
		Error:   errorMsg,
	})
}

// BadGateway - 502
func BadGateway(c echo.Context, message string, err error) error {
	errorMsg := message
	if err != nil {
		errorMsg = err.Error()
	}

	return c.JSON(http.StatusBadGateway, Response{
		Success: false,
		Error:   errorMsg,
	})
}

// ServiceUnavailable - 503
func ServiceUnavailable(c echo.Context, message string, err error) error {
	errorMsg := message
	if err != nil {
		errorMsg = err.Error()
	}

	return c.JSON(http.StatusServiceUnavailable, Response{
		Success: false,
		Error:   errorMsg,
	})
}

// GatewayTimeout - 504
func GatewayTimeout(c echo.Context, message string, err error) error {
	errorMsg := message
	if err != nil {
		errorMsg = err.Error()
	}

	return c.JSON(http.StatusGatewayTimeout, Response{
		Success: false,
		Error:   errorMsg,
	})
}

// ==================== SPECIALIZED RESPONSES ====================

// Paginated creates a paginated response
func Paginated(c echo.Context, message string, data interface{}, page, perPage, totalCount int) error {
	totalPages := (totalCount + perPage - 1) / perPage // Ceiling division

	return c.JSON(http.StatusOK, PaginatedResponse{
		Response: Response{
			Success: true,
			Message: message,
			Data:    data,
		},
		Pagination: &Pagination{
			Page:       page,
			PerPage:    perPage,
			TotalCount: totalCount,
			TotalPages: totalPages,
		},
	})
}

// FileResponse sends a file as response
func FileResponse(c echo.Context, file []byte, filename string, inline bool) error {
	disposition := "attachment"
	if inline {
		disposition = "inline"
	}

	c.Response().Header().Set("Content-Disposition", disposition+"; filename=\""+filename+"\"")
	return c.Blob(http.StatusOK, http.DetectContentType(file), file)
}

// StreamResponse for streaming data
func StreamResponse(c echo.Context, contentType string, reader io.Reader) error {
	c.Response().Header().Set("Content-Type", contentType)
	c.Response().Header().Set("Transfer-Encoding", "chunked")
	c.Response().WriteHeader(http.StatusOK)

	_, err := io.Copy(c.Response(), reader)
	return err
}

// HTMLResponse for HTML content
func HTMLResponse(c echo.Context, status int, html string) error {
	return c.HTML(status, html)
}

// XMLResponse for XML content
func XMLResponse(c echo.Context, status int, data interface{}) error {
	return c.XML(status, data)
}

// RedirectResponse for redirects
func RedirectResponse(c echo.Context, status int, url string) error {
	return c.Redirect(status, url)
}

// ==================== HELPER FUNCTIONS ====================

// Success is an alias for OK for backward compatibility
func Success(c echo.Context, message string, data interface{}) error {
	return OK(c, message, data)
}

// Error is a generic error response that determines status code from error type
func Error(c echo.Context, err error) error {
	switch e := err.(type) {
	case *echo.HTTPError:
		return c.JSON(e.Code, Response{
			Success: false,
			Error:   e.Message.(string),
		})
	default:
		return InternalServerError(c, "Internal server error", err)
	}
}

// ValidationError for validation failures
func ValidationError(c echo.Context, message string, validationErrors map[string]string) error {
	return c.JSON(http.StatusUnprocessableEntity, Response{
		Success: false,
		Error:   message,
		Data:    validationErrors,
	})
}

// WithMeta adds metadata to response
func WithMeta(c echo.Context, status int, message string, data, meta interface{}) error {
	return c.JSON(status, Response{
		Success: true,
		Message: message,
		Data:    data,
		Meta:    meta,
	})
}

// HealthCheck is a specialized health check response
func HealthCheck(c echo.Context, status string, details map[string]interface{}) error {
	healthData := map[string]interface{}{
		"status":    status,
		"timestamp": time.Now().UTC(),
		"version":   "1.0.0", // This should come from config
	}

	for k, v := range details {
		healthData[k] = v
	}

	return OK(c, "Service health status", healthData)
}

// EmptySuccess for when you only need success status without data
func EmptySuccess(c echo.Context, message string) error {
	return OK(c, message, nil)
}
