// Package common holds shared non-protocol helpers: configuration,
// structured errors, path validation, and disk arithmetic.
package common

import "fmt"

// AppError is a structured, log-friendly error with a machine-readable code.
type AppError struct {
	Code    string
	Message string
	Cause   error
}

func (e *AppError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *AppError) Unwrap() error { return e.Cause }

// E builds an *AppError.
func E(code, message string, cause error) *AppError {
	return &AppError{Code: code, Message: message, Cause: cause}
}

// Common error codes.
const (
	CodeInvalidInput = "INVALID_INPUT"
	CodeNotFound     = "NOT_FOUND"
	CodeConflict     = "CONFLICT"
	CodeFenced       = "FENCED"
	CodeNoCapacity   = "NO_CAPACITY"
	CodeStorage      = "STORAGE"
	CodeUnauthorized = "UNAUTHORIZED"
	CodeInternal     = "INTERNAL"
	CodePrecondition = "PRECONDITION"
)
