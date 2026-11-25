// Package usecase holds the business logic. Each type depends only on domain
// interfaces so it can be unit tested without a database.
package usecase

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog"

	"github.com/alfateve/stayin-local-backend/internal/domain"
	"github.com/alfateve/stayin-local-backend/internal/platform/auth"
)

// Auth handles admin login, refresh and profile operations.
type Auth struct {
	users  domain.UserRepository
	tokens *auth.Manager
	audit  domain.AuditRepository
	log    zerolog.Logger
}

// NewAuth builds the auth usecase.
func NewAuth(users domain.UserRepository, tokens *auth.Manager, audit domain.AuditRepository, log zerolog.Logger) *Auth {
	return &Auth{
		users:  users,
		tokens: tokens,
		audit:  audit,
		log:    log.With().Str("usecase", "auth").Logger(),
	}
}

// Login validates credentials and issues a token pair.
func (u *Auth) Login(ctx context.Context, req domain.LoginRequest, ip string) (*domain.LoginResponse, error) {
	if req.Email == "" || req.Password == "" {
		return nil, domain.NewValidation("email", "email dan password wajib diisi")
	}

	user, err := u.users.GetByEmail(ctx, req.Email)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			// Do not disclose whether the account exists.
			return nil, fmt.Errorf("login: %w", domain.ErrUnauthorized)
		}
		return nil, err
	}

	if user.Status != "ACTIVE" {
		return nil, fmt.Errorf("login: akun dinonaktifkan: %w", domain.ErrForbidden)
	}

	if err := auth.CheckPassword(user.PasswordHash, req.Password); err != nil {
		return nil, fmt.Errorf("login: %w", domain.ErrUnauthorized)
	}

	access, refresh, expiresIn, err := u.tokens.Issue(user)
	if err != nil {
		return nil, err
	}

	if err := u.users.TouchLogin(ctx, user.ID); err != nil {
		u.log.Warn().Err(err).Msg("touch login failed")
	}

	u.recordAudit(ctx, user, "AUTH_LOGIN", "user", user.ID, ip)

	return &domain.LoginResponse{
		AccessToken:  access,
		RefreshToken: refresh,
		TokenType:    "Bearer",
		ExpiresIn:    expiresIn,
		User:         *user,
	}, nil
}

// Refresh exchanges a refresh token for a new pair.
func (u *Auth) Refresh(ctx context.Context, refreshToken string) (*domain.LoginResponse, error) {
	claims, err := u.tokens.VerifyRefresh(refreshToken)
	if err != nil {
		return nil, fmt.Errorf("refresh: %w", domain.ErrUnauthorized)
	}

	user, err := u.users.GetUserByID(ctx, claims.UserID)
	if err != nil {
		return nil, fmt.Errorf("refresh: %w", domain.ErrUnauthorized)
	}
	if user.Status != "ACTIVE" {
		return nil, fmt.Errorf("refresh: %w", domain.ErrForbidden)
	}

	access, refresh, expiresIn, err := u.tokens.Issue(user)
	if err != nil {
		return nil, err
	}

	return &domain.LoginResponse{
		AccessToken:  access,
		RefreshToken: refresh,
		TokenType:    "Bearer",
		ExpiresIn:    expiresIn,
		User:         *user,
	}, nil
}

// Verify resolves the identity behind an access token.
func (u *Auth) Verify(ctx context.Context, accessToken string) (*domain.AuthClaims, error) {
	claims, err := u.tokens.Verify(accessToken)
	if err != nil {
		return nil, fmt.Errorf("verify: %w", domain.ErrUnauthorized)
	}

	user, err := u.users.GetUserByID(ctx, claims.UserID)
	if err != nil {
		return nil, fmt.Errorf("verify: %w", domain.ErrUnauthorized)
	}
	if user.Status != "ACTIVE" {
		return nil, fmt.Errorf("verify: %w", domain.ErrForbidden)
	}

	tenantID := ""
	if user.TenantID != nil {
		tenantID = *user.TenantID
	}

	return &domain.AuthClaims{
		UserID:   user.ID,
		TenantID: tenantID,
		Role:     user.Role,
		Email:    user.Email,
	}, nil
}

// Profile returns the authenticated account.
func (u *Auth) Profile(ctx context.Context, userID string) (*domain.User, error) {
	return u.users.GetUserByID(ctx, userID)
}

// ChangePassword updates the caller's password after verifying the old one.
func (u *Auth) ChangePassword(ctx context.Context, userID, oldPassword, newPassword, ip string) error {
	user, err := u.users.GetUserByID(ctx, userID)
	if err != nil {
		return err
	}
	if err := auth.CheckPassword(user.PasswordHash, oldPassword); err != nil {
		return domain.NewValidation("old_password", "password lama tidak sesuai")
	}
	hash, err := auth.HashPassword(newPassword)
	if err != nil {
		return err
	}
	if err := u.users.UpdatePassword(ctx, userID, hash); err != nil {
		return err
	}
	u.recordAudit(ctx, user, "AUTH_CHANGE_PASSWORD", "user", userID, ip)
	return nil
}

func (u *Auth) recordAudit(ctx context.Context, user *domain.User, action, entity, entityID, ip string) {
	actorID := user.ID
	var tenantID *string
	if user.TenantID != nil {
		tenantID = user.TenantID
	}
	entry := &domain.AuditLog{
		TenantID:  tenantID,
		ActorID:   &actorID,
		ActorRole: string(user.Role),
		Action:    action,
		Entity:    entity,
		EntityID:  entityID,
		IP:        ip,
	}
	if err := u.audit.CreateAuditLog(ctx, entry); err != nil {
		u.log.Warn().Err(err).Msg("write audit log failed")
	}
}

// EnsureBootstrap creates the first accounts when the table is empty.
func (u *Auth) EnsureBootstrap(ctx context.Context, tenants domain.TenantRepository, superAdmin, tenantAdmin domain.UserUpsertRequest, tenantSlug string) error {
	count, err := u.users.CountUsers(ctx)
	if err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	tenant, err := tenants.GetBySlug(ctx, tenantSlug)
	if err != nil {
		return fmt.Errorf("bootstrap: resolve tenant %q: %w", tenantSlug, err)
	}

	superHash, err := auth.HashPassword(superAdmin.Password)
	if err != nil {
		return err
	}
	superUser := &domain.User{
		Email:        superAdmin.Email,
		PasswordHash: superHash,
		FullName:     superAdmin.FullName,
		Role:         domain.RoleSuperAdmin,
		Status:       "ACTIVE",
	}
	if err := u.users.CreateUser(ctx, superUser); err != nil {
		return fmt.Errorf("bootstrap: create super admin: %w", err)
	}

	tenantHash, err := auth.HashPassword(tenantAdmin.Password)
	if err != nil {
		return err
	}
	tenantID := tenant.ID
	tenantUser := &domain.User{
		TenantID:     &tenantID,
		Email:        tenantAdmin.Email,
		PasswordHash: tenantHash,
		FullName:     tenantAdmin.FullName,
		Role:         domain.RoleTenantAdmin,
		Status:       "ACTIVE",
	}
	if err := u.users.CreateUser(ctx, tenantUser); err != nil {
		return fmt.Errorf("bootstrap: create tenant admin: %w", err)
	}

	u.log.Info().
		Str("super_admin", superUser.Email).
		Str("tenant_admin", tenantUser.Email).
		Str("tenant", tenant.Slug).
		Msg("bootstrap accounts created")

	return nil
}

var _ = time.Now
