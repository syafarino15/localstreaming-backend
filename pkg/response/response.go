// Package response defines the single JSON envelope every endpoint returns,
// as required by Documents/rules_guidelines_fe_be.md.
package response

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Meta carries pagination information for list endpoints.
type Meta struct {
	Page      int   `json:"page"`
	Limit     int   `json:"limit"`
	TotalData int64 `json:"total_data"`
	TotalPage int   `json:"total_page"`
}

// FieldError describes a single validation failure.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Envelope is the success response shape.
type Envelope struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
	Meta    *Meta  `json:"meta,omitempty"`
}

// ErrorEnvelope is the failure response shape.
type ErrorEnvelope struct {
	Success bool         `json:"success"`
	Message string       `json:"message"`
	Errors  []FieldError `json:"errors,omitempty"`
}

// OK writes a 200 success envelope.
func OK(c *gin.Context, message string, data any) {
	c.JSON(http.StatusOK, Envelope{Success: true, Message: message, Data: data})
}

// Created writes a 201 success envelope.
func Created(c *gin.Context, message string, data any) {
	c.JSON(http.StatusCreated, Envelope{Success: true, Message: message, Data: data})
}

// Paged writes a 200 success envelope with pagination metadata.
func Paged(c *gin.Context, message string, data any, meta Meta) {
	c.JSON(http.StatusOK, Envelope{Success: true, Message: message, Data: data, Meta: &meta})
}

// NoContent writes a 200 success envelope without data.
func NoContent(c *gin.Context, message string) {
	c.JSON(http.StatusOK, Envelope{Success: true, Message: message})
}

// Error writes a failure envelope with the given status code.
func Error(c *gin.Context, status int, message string, fields ...FieldError) {
	c.JSON(status, ErrorEnvelope{Success: false, Message: message, Errors: fields})
}

// BadRequest writes a 400 failure envelope.
func BadRequest(c *gin.Context, message string, fields ...FieldError) {
	Error(c, http.StatusBadRequest, message, fields...)
}

// Unauthorized writes a 401 failure envelope.
func Unauthorized(c *gin.Context, message string) {
	Error(c, http.StatusUnauthorized, message)
}

// Forbidden writes a 403 failure envelope.
func Forbidden(c *gin.Context, message string) {
	Error(c, http.StatusForbidden, message)
}

// NotFound writes a 404 failure envelope.
func NotFound(c *gin.Context, message string) {
	Error(c, http.StatusNotFound, message)
}

// Conflict writes a 409 failure envelope.
func Conflict(c *gin.Context, message string, fields ...FieldError) {
	Error(c, http.StatusConflict, message, fields...)
}

// Internal writes a 500 failure envelope.
func Internal(c *gin.Context, message string) {
	Error(c, http.StatusInternalServerError, message)
}

// BuildMeta computes pagination metadata from the total row count.
func BuildMeta(page, limit int, total int64) Meta {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 10
	}
	totalPage := int((total + int64(limit) - 1) / int64(limit))
	if totalPage == 0 {
		totalPage = 1
	}
	return Meta{Page: page, Limit: limit, TotalData: total, TotalPage: totalPage}
}
