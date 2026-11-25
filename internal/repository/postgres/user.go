package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/alfateve/stayin-local-backend/internal/domain"
)

const userColumns = `id, tenant_id, email, password_hash, full_name, role, status, last_login_at, created_at, updated_at`

func scanUser(row pgx.Row) (*domain.User, error) {
	var (
		u domain.User
	)
	err := row.Scan(&u.ID, &u.TenantID, &u.Email, &u.PasswordHash, &u.FullName, &u.Role, &u.Status, &u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, mapWriteError("user", err)
	}
	return &u, nil
}

// GetByEmail returns the account for a login attempt.
func (s *Store) GetByEmail(ctx context.Context, email string) (*domain.User, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM mst_users WHERE lower(email) = lower($1)`, email)
	return scanUser(row)
}

// GetByID returns the account identified by primary key.
func (s *Store) GetUserByID(ctx context.Context, id string) (*domain.User, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM mst_users WHERE id = $1`, id)
	return scanUser(row)
}

// List returns accounts. An empty tenantID lists every account, which is the
// super admin view.
func (s *Store) ListUsers(ctx context.Context, tenantID string, p domain.Pagination) ([]domain.User, int64, error) {
	limit, offset := paginate(p.Page, p.Limit)

	var (
		total int64
		rows  pgx.Rows
		err   error
	)
	if tenantID == "" {
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM mst_users`).Scan(&total); err != nil {
			return nil, 0, mapWriteError("user", err)
		}
		rows, err = s.pool.Query(ctx, `SELECT `+userColumns+` FROM mst_users ORDER BY created_at DESC LIMIT $1 OFFSET $2`, limit, offset)
	} else {
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM mst_users WHERE tenant_id = $1`, tenantID).Scan(&total); err != nil {
			return nil, 0, mapWriteError("user", err)
		}
		rows, err = s.pool.Query(ctx, `SELECT `+userColumns+` FROM mst_users WHERE tenant_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`, tenantID, limit, offset)
	}
	if err != nil {
		return nil, 0, mapWriteError("user", err)
	}
	defer rows.Close()

	out := make([]domain.User, 0, limit)
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *u)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, mapWriteError("user", err)
	}
	return out, total, nil
}

// Create inserts an account.
func (s *Store) CreateUser(ctx context.Context, u *domain.User) error {
	if u.Status == "" {
		u.Status = "ACTIVE"
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO mst_users (tenant_id, email, password_hash, full_name, role, status)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+userColumns,
		u.TenantID, u.Email, u.PasswordHash, u.FullName, u.Role, u.Status)
	created, err := scanUser(row)
	if err != nil {
		return err
	}
	*u = *created
	return nil
}

// Update rewrites the mutable account fields except the password.
func (s *Store) UpdateUser(ctx context.Context, u *domain.User) error {
	row := s.pool.QueryRow(ctx, `
		UPDATE mst_users
		SET email = $2, full_name = $3, role = $4, status = $5, tenant_id = $6
		WHERE id = $1
		RETURNING `+userColumns,
		u.ID, u.Email, u.FullName, u.Role, u.Status, u.TenantID)
	updated, err := scanUser(row)
	if err != nil {
		return err
	}
	*u = *updated
	return nil
}

// UpdatePassword sets a new password hash.
func (s *Store) UpdatePassword(ctx context.Context, id, passwordHash string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE mst_users SET password_hash = $2 WHERE id = $1`, id, passwordHash)
	if err != nil {
		return mapWriteError("user", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.WrapNotFound("user")
	}
	return nil
}

// TouchLogin records a successful authentication.
func (s *Store) TouchLogin(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `UPDATE mst_users SET last_login_at = now() WHERE id = $1`, id)
	return mapWriteError("user", err)
}

// Count returns the number of accounts, used for bootstrap detection.
func (s *Store) CountUsers(ctx context.Context) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM mst_users`).Scan(&n)
	return n, mapWriteError("user", err)
}

var _ = time.Time{}
