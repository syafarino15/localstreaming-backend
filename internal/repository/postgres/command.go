package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/alfateve/stayin-local-backend/internal/domain"
)

const commandColumns = `id, tenant_id, device_id, command, payload, status, issued_by, issued_at, acked_at, error`

func scanCommand(row pgx.Row) (*domain.RemoteCommand, error) {
	var (
		c       domain.RemoteCommand
		payload []byte
	)
	err := row.Scan(&c.ID, &c.TenantID, &c.DeviceID, &c.Command, &payload, &c.Status,
		&c.IssuedBy, &c.IssuedAt, &c.AckedAt, &c.Error)
	if err != nil {
		return nil, mapWriteError("remote command", err)
	}
	c.Payload = rawJSON(payload)
	return &c, nil
}

// Create inserts a queued command.
func (l *Local) CreateCommand(ctx context.Context, c *domain.RemoteCommand) error {
	payload, err := marshalJSON(c.Payload)
	if err != nil {
		return err
	}
	var issuedBy any
	if c.IssuedBy != nil && *c.IssuedBy != "" {
		issuedBy = *c.IssuedBy
	}
	row := l.pool.QueryRow(ctx, `
		INSERT INTO trn_remote_commands (tenant_id, device_id, command, payload, status, issued_by)
		VALUES ($1, $2, $3, $4::jsonb, 'QUEUED', $5)
		RETURNING `+commandColumns,
		c.TenantID, c.DeviceID, c.Command, payload, issuedBy)
	created, err := scanCommand(row)
	if err != nil {
		return err
	}
	*c = *created
	return nil
}

// List returns command history. An empty tenantID lists every command.
func (l *Local) ListCommands(ctx context.Context, tenantID string, p domain.Pagination) ([]domain.RemoteCommand, int64, error) {
	limit, offset := paginate(p.Page, p.Limit)

	var (
		total int64
		rows  pgx.Rows
		err   error
	)
	if tenantID == "" {
		if err := l.pool.QueryRow(ctx, `SELECT count(*) FROM trn_remote_commands`).Scan(&total); err != nil {
			return nil, 0, mapWriteError("remote command", err)
		}
		rows, err = l.pool.Query(ctx, `SELECT `+commandColumns+` FROM trn_remote_commands ORDER BY issued_at DESC LIMIT $1 OFFSET $2`, limit, offset)
	} else {
		if err := l.pool.QueryRow(ctx, `SELECT count(*) FROM trn_remote_commands WHERE tenant_id = $1`, tenantID).Scan(&total); err != nil {
			return nil, 0, mapWriteError("remote command", err)
		}
		rows, err = l.pool.Query(ctx, `SELECT `+commandColumns+` FROM trn_remote_commands WHERE tenant_id = $1 ORDER BY issued_at DESC LIMIT $2 OFFSET $3`, tenantID, limit, offset)
	}
	if err != nil {
		return nil, 0, mapWriteError("remote command", err)
	}
	defer rows.Close()

	out := make([]domain.RemoteCommand, 0, limit)
	for rows.Next() {
		c, err := scanCommand(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *c)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, mapWriteError("remote command", err)
	}
	return out, total, nil
}

// GetByID returns one command.
func (l *Local) GetCommandByID(ctx context.Context, id string) (*domain.RemoteCommand, error) {
	row := l.pool.QueryRow(ctx, `SELECT `+commandColumns+` FROM trn_remote_commands WHERE id = $1`, id)
	return scanCommand(row)
}

// ListPendingForDevice returns commands not yet acknowledged. A TV calls this
// right after reconnecting, which is what makes offline delivery work.
func (l *Local) ListPendingForDevice(ctx context.Context, deviceID string) ([]domain.RemoteCommand, error) {
	rows, err := l.pool.Query(ctx, `
		SELECT `+commandColumns+`
		FROM trn_remote_commands
		WHERE device_id = $1 AND status IN ('QUEUED', 'SENT')
		ORDER BY issued_at
		LIMIT 50`, deviceID)
	if err != nil {
		return nil, mapWriteError("remote command", err)
	}
	defer rows.Close()

	out := make([]domain.RemoteCommand, 0, 8)
	for rows.Next() {
		c, err := scanCommand(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, nil
}

// MarkSent records that the command left the server.
func (l *Local) MarkSent(ctx context.Context, id string) error {
	_, err := l.pool.Exec(ctx, `UPDATE trn_remote_commands SET status = 'SENT' WHERE id = $1 AND status = 'QUEUED'`, id)
	return mapWriteError("remote command", err)
}

// MarkAcked records that the TV confirmed execution.
func (l *Local) MarkAcked(ctx context.Context, id string) error {
	_, err := l.pool.Exec(ctx, `
		UPDATE trn_remote_commands
		SET status = 'ACKED', acked_at = now()
		WHERE id = $1 AND status IN ('QUEUED', 'SENT')`, id)
	return mapWriteError("remote command", err)
}

// MarkFailed records a delivery failure.
func (l *Local) MarkFailed(ctx context.Context, id, message string) error {
	_, err := l.pool.Exec(ctx, `
		UPDATE trn_remote_commands
		SET status = 'FAILED', error = $2, acked_at = now()
		WHERE id = $1`, id, message)
	return mapWriteError("remote command", err)
}
