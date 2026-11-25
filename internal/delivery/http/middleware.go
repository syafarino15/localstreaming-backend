// Package http wires the HTTP surface: middleware, handlers and router.
package http

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/alfateve/stayin-local-backend/internal/domain"
	"github.com/alfateve/stayin-local-backend/internal/platform/auth"
	"github.com/alfateve/stayin-local-backend/pkg/response"
)

// Context keys.
const (
	ctxClaims    = "auth.claims"
	ctxDevice    = "auth.device"
	ctxRequestID = "request.id"
)

// RequestID assigns a correlation id to every request.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader("X-Request-Id")
		if id == "" {
			id = uuid.NewString()
		}
		c.Set(ctxRequestID, id)
		c.Header("X-Request-Id", id)
		c.Next()
	}
}

// Logger emits one structured line per request.
func Logger(log zerolog.Logger) gin.HandlerFunc {
	logger := log.With().Str("component", "http").Logger()
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		query := c.Request.URL.RawQuery

		c.Next()

		entry := logger.With().
			Str("request_id", c.GetString(ctxRequestID)).
			Str("method", c.Request.Method).
			Str("path", path).
			Int("status", c.Writer.Status()).
			Dur("latency", time.Since(start)).
			Str("ip", c.ClientIP()).
			Logger()

		if query != "" {
			entry = entry.With().Str("query", query).Logger()
		}

		switch {
		case c.Writer.Status() >= 500:
			// Attach the underlying cause so an unexpected driver or scan
			// error is diagnosable from the log without a debugger.
			if len(c.Errors) > 0 {
				entry = entry.With().Str("cause", c.Errors.String()).Logger()
			}
			entry.Error().Msg("request failed")
		case c.Writer.Status() >= 400:
			entry.Warn().Msg("request rejected")
		default:
			entry.Info().Msg("request completed")
		}
	}
}

// Recovery converts a panic into a structured 500 response.
func Recovery(log zerolog.Logger) gin.HandlerFunc {
	logger := log.With().Str("component", "recovery").Logger()
	return func(c *gin.Context) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.Error().
					Interface("panic", recovered).
					Str("path", c.Request.URL.Path).
					Msg("panic recovered")
				response.Internal(c, "terjadi kesalahan internal")
				c.Abort()
			}
		}()
		c.Next()
	}
}

// CORS allows the configured CMS origins only.
func CORS(allowedOrigins []string) gin.HandlerFunc {
	allowed := make(map[string]struct{}, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		allowed[strings.ToLower(strings.TrimSpace(origin))] = struct{}{}
	}

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin != "" {
			if _, ok := allowed[strings.ToLower(origin)]; ok {
				c.Header("Access-Control-Allow-Origin", origin)
				c.Header("Vary", "Origin")
				c.Header("Access-Control-Allow-Credentials", "true")
				c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Device-Token, X-Request-Id")
				c.Header("Access-Control-Expose-Headers", "Content-Range, Accept-Ranges, Content-Length, X-Request-Id")
				c.Header("Access-Control-Max-Age", "600")
			}
		}

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// Verifier is the token verification contract used by the admin middleware.
type Verifier interface {
	Verify(ctx context.Context, token string) (*domain.AuthClaims, error)
}

// AdminAuth authenticates an admin request from the Authorization header or the
// httpOnly access cookie.
func AdminAuth(verifier Verifier) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := bearerToken(c)
		if token == "" {
			if cookie, err := c.Cookie("access_token"); err == nil {
				token = cookie
			}
		}
		if token == "" {
			response.Unauthorized(c, "sesi tidak ditemukan, silakan login")
			c.Abort()
			return
		}

		claims, err := verifier.Verify(c.Request.Context(), token)
		if err != nil {
			response.Unauthorized(c, "sesi tidak valid atau sudah berakhir")
			c.Abort()
			return
		}

		c.Set(ctxClaims, claims)
		c.Next()
	}
}

// RequireRole restricts a route group to the given roles.
func RequireRole(roles ...domain.Role) gin.HandlerFunc {
	allowed := make(map[domain.Role]struct{}, len(roles))
	for _, role := range roles {
		allowed[role] = struct{}{}
	}

	return func(c *gin.Context) {
		claims := ClaimsFrom(c)
		if claims == nil {
			response.Unauthorized(c, "sesi tidak valid")
			c.Abort()
			return
		}
		if _, ok := allowed[claims.Role]; !ok {
			response.Forbidden(c, "anda tidak memiliki akses ke menu ini")
			c.Abort()
			return
		}
		c.Next()
	}
}

// RequireTenant rejects a tenant scoped route when the session has no tenant.
func RequireTenant() gin.HandlerFunc {
	return func(c *gin.Context) {
		claims := ClaimsFrom(c)
		if claims == nil || claims.TenantID == "" {
			response.Forbidden(c, "sesi ini tidak terikat pada tenant manapun")
			c.Abort()
			return
		}
		c.Next()
	}
}

// DeviceAuth resolves the TV from its bearer token. The device_id query, when
// present, must match the token owner so a token cannot read another cabin.
func DeviceAuth(devices domain.DeviceRepository) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := strings.TrimSpace(c.GetHeader("X-Device-Token"))
		if token == "" {
			token = c.Query("token")
		}
		if token == "" {
			token = bearerToken(c)
		}
		if token == "" {
			response.Unauthorized(c, "token perangkat wajib dikirim pada header X-Device-Token")
			c.Abort()
			return
		}

		device, err := devices.GetByTokenHash(c.Request.Context(), auth.HashToken(token))
		if err != nil {
			response.Unauthorized(c, "token perangkat tidak valid")
			c.Abort()
			return
		}
		if device.Status == domain.DeviceDisabled {
			response.Forbidden(c, "perangkat ini dinonaktifkan oleh administrator")
			c.Abort()
			return
		}

		if requested := c.Query("device_id"); requested != "" && requested != device.DeviceUID && requested != device.ID {
			response.Forbidden(c, "device_id tidak sesuai dengan token perangkat")
			c.Abort()
			return
		}

		c.Set(ctxDevice, device)
		c.Next()
	}
}

// ClaimsFrom returns the authenticated admin identity.
func ClaimsFrom(c *gin.Context) *domain.AuthClaims {
	value, ok := c.Get(ctxClaims)
	if !ok {
		return nil
	}
	claims, ok := value.(*domain.AuthClaims)
	if !ok {
		return nil
	}
	return claims
}

// DeviceFrom returns the authenticated TV.
func DeviceFrom(c *gin.Context) *domain.Device {
	value, ok := c.Get(ctxDevice)
	if !ok {
		return nil
	}
	device, ok := value.(*domain.Device)
	if !ok {
		return nil
	}
	return device
}

// RequestIDFrom returns the correlation id.
func RequestIDFrom(c *gin.Context) string { return c.GetString(ctxRequestID) }

func bearerToken(c *gin.Context) string {
	header := c.GetHeader("Authorization")
	if header == "" {
		return ""
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

// MapError converts a usecase error into the right HTTP response.
func MapError(c *gin.Context, err error) {
	if err == nil {
		return
	}

	var validation *domain.ValidationError
	if errors.As(err, &validation) {
		fields := make([]response.FieldError, 0, len(validation.Fields))
		for _, f := range validation.Fields {
			fields = append(fields, response.FieldError{Field: f.Field, Message: f.Message})
		}
		response.BadRequest(c, "validasi gagal", fields...)
		return
	}

	switch {
	case errors.Is(err, domain.ErrValidation):
		response.BadRequest(c, "validasi gagal")
	case errors.Is(err, domain.ErrUnauthorized):
		response.Unauthorized(c, "kredensial tidak valid")
	case errors.Is(err, domain.ErrForbidden):
		response.Forbidden(c, "akses ditolak")
	case errors.Is(err, domain.ErrNotFound):
		response.NotFound(c, "data tidak ditemukan")
	case errors.Is(err, domain.ErrConflict):
		response.Conflict(c, "data sudah ada")
	case errors.Is(err, domain.ErrGone):
		response.Error(c, http.StatusGone, "data sudah kedaluwarsa")
	case errors.Is(err, domain.ErrUnavailable):
		response.Error(c, http.StatusServiceUnavailable, "layanan belum tersedia")
	default:
		_ = c.Error(err)
		response.Internal(c, "terjadi kesalahan pada server")
	}
}
