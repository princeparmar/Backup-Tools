package mstenant

import (
	"errors"
	"net/http"
)

// Error codes returned to Satellite in the JSON body ("code").
const (
	CodeCredentialNotFound       = "credential_not_found"
	CodeCredentialForbidden      = "credential_forbidden"
	CodeTenantNotLinked          = "tenant_not_linked"
	CodeTenantDisconnected       = "tenant_disconnected"
	CodeTenantMismatch           = "tenant_mismatch"
	CodeTenantUnavailable        = "tenant_unavailable"
	CodeOrganizationModeRequired = "organization_mode_required"
	CodeConsentRequired          = "consent_required"
	CodeCapabilityDenied         = "capability_denied"
	CodeSigninRequired           = "signin_required"
	CodeAppSecretExpired         = "app_secret_expired"
	CodeTemporary                = "temporary"
)

// Error is a resolver refusal with the HTTP status handlers should return.
type Error struct {
	HTTPStatus int
	Code       string
	Capability string
	Message    string
}

func (e *Error) Error() string { return e.Message }

// Body is the JSON error payload for HTTP handlers.
func (e *Error) Body() map[string]interface{} {
	body := map[string]interface{}{"error": e.Message, "code": e.Code}
	if e.Capability != "" {
		body["capability"] = e.Capability
	}
	return body
}

// AsError unwraps a resolver error.
func AsError(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// IsCode reports whether err is a resolver error with code.
func IsCode(err error, code string) bool {
	e, ok := AsError(err)
	return ok && e.Code == code
}

func newError(status int, code, msg string) *Error {
	return &Error{HTTPStatus: status, Code: code, Message: msg}
}

var statusForCode = map[string]int{
	CodeCredentialNotFound:       http.StatusNotFound,
	CodeCredentialForbidden:      http.StatusForbidden,
	CodeTenantNotLinked:          http.StatusNotFound,
	CodeTenantDisconnected:       http.StatusConflict,
	CodeTenantMismatch:           http.StatusForbidden,
	CodeTenantUnavailable:        http.StatusConflict,
	CodeOrganizationModeRequired: http.StatusForbidden,
	CodeConsentRequired:          http.StatusForbidden,
	CodeCapabilityDenied:         http.StatusForbidden,
	CodeSigninRequired:           http.StatusForbidden,
	CodeAppSecretExpired:         http.StatusServiceUnavailable,
	CodeTemporary:                http.StatusServiceUnavailable,
}

func codeError(code, msg string) *Error {
	return newError(statusForCode[code], code, msg)
}
