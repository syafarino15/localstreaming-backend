package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/alfateve/stayin-local-backend/internal/domain"
)

const auditColumns = `id, tenant_id, actor_id, actor_role, action, entity, entity_id, ip, metadata, created_at`

func scanAudit(row pgx.Row) (*domain.AuditLog, error) {
	var (
		a    domain.AuditLog
		meta []byte
	)
	err := row.Scan(&a.ID, &a.TenantID, &a.ActorID, &a.ActorRole, &a.Action, &a.Entity,
		&a.EntityID, &a.IP, &meta, &a.CreatedAt)
	if err != nil {
		return nil, mapWriteError("audit log", err)
	}
	a.Metadata = rawJSON(meta)
	return &a, nil
}

// Create appends an audit entry. Rows are never updated or deleted.
func (s *Store) CreateAuditLog(ctx context.Context, a *domain.AuditLog) error {
	meta, err := marshalJSON(a.Metadata)
	if err != nil {
		return err
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO audit_logs (tenant_id, actor_id, actor_role, action, entity, entity_id, ip, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb)
		RETURNING `+auditColumns,
		a.TenantID, a.ActorID, a.ActorRole, a.Action, a.Entity, a.EntityID, a.IP, meta)
	created, err := scanAudit(row)
	if err != nil {
		return err
	}
	*a = *created
	return nil
}

// List returns the audit trail, newest first. An empty tenantID lists every
// entry, which is the super admin view.
func (s *Store) ListAuditLogs(ctx context.Context, tenantID string, p domain.Pagination) ([]domain.AuditLog, int64, error) {
	limit, offset := paginate(p.Page, p.Limit)

	var (
		total int64
		rows  pgx.Rows
		err   error
	)
	if tenantID == "" {
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs`).Scan(&total); err != nil {
			return nil, 0, mapWriteError("audit log", err)
		}
		rows, err = s.pool.Query(ctx, `SELECT `+auditColumns+` FROM audit_logs ORDER BY created_at DESC LIMIT $1 OFFSET $2`, limit, offset)
	} else {
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE tenant_id = $1`, tenantID).Scan(&total); err != nil {
			return nil, 0, mapWriteError("audit log", err)
		}
		rows, err = s.pool.Query(ctx, `SELECT `+auditColumns+` FROM audit_logs WHERE tenant_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`, tenantID, limit, offset)
	}
	if err != nil {
		return nil, 0, mapWriteError("audit log", err)
	}
	defer rows.Close()

	out := make([]domain.AuditLog, 0, limit)
	for rows.Next() {
		a, err := scanAudit(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *a)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, mapWriteError("audit log", err)
	}
	return out, total, nil
}
