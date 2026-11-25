package domain

import (
	"errors"
	"fmt"
)

// Sentinel errors. Usecases return these so the HTTP layer can map them to
// status codes without knowing anything about the database.
var (
	ErrNotFound     = errors.New("resource not found")
	ErrUnauthorized = errors.New("unauthorized")
	ErrForbidden    = errors.New("forbidden")
	ErrConflict     = errors.New("resource conflict")
	ErrValidation   = errors.New("validation failed")
	ErrGone         = errors.New("resource expired")
	ErrUnavailable  = errors.New("service unavailable")
)

// FieldError carries a machine readable validation failure.
type FieldError struct {
	Field   string
	Message string
}

// ValidationError aggregates one or more field failures.
type ValidationError struct {
	Fields []FieldError
}

func (e *ValidationError) Error() string {
	if len(e.Fields) == 0 {
		return "validation failed"
	}
	return fmt.Sprintf("validation failed: %s: %s", e.Fields[0].Field, e.Fields[0].Message)
}

// Unwrap lets errors.Is(err, ErrValidation) succeed.
func (e *ValidationError) Unwrap() error { return ErrValidation }

// NewValidation builds a ValidationError from a single field failure.
func NewValidation(field, message string) *ValidationError {
	return &ValidationError{Fields: []FieldError{{Field: field, Message: message}}}
}

// AddField appends a field failure, creating the container when needed.
func (e *ValidationError) AddField(field, message string) *ValidationError {
	e.Fields = append(e.Fields, FieldError{Field: field, Message: message})
	return e
}

// WrapNotFound annotates a not found error with context.
func WrapNotFound(entity string) error {
	return fmt.Errorf("%s: %w", entity, ErrNotFound)
}
