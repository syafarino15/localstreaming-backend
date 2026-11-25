package postgres

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/alfateve/stayin-local-backend/internal/domain"
)

const deviceColumns = `id, tenant_id, room_id, device_uid, label, model, app_version, pairing_code, pairing_expires_at, device_token_hash, status, ip_address, last_seen_at, created_at, updated_at`

func scanDevice(row pgx.Row) (*domain.Device, error) {
	var d domain.Device
	err := row.Scan(&d.ID, &d.TenantID, &d.RoomID, &d.DeviceUID, &d.Label, &d.Model, &d.AppVersion,
		&d.PairingCode, &d.PairingExpiresAt, &d.DeviceTokenHash, &d.Status, &d.IPAddress, &d.LastSeenAt, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return nil, mapWriteError("device", err)
	}
	return &d, nil
}

// GetByUID returns the device bound to a hardware identifier.
func (l *Local) GetByUID(ctx context.Context, deviceUID string) (*domain.Device, error) {
	row := l.pool.QueryRow(ctx, `SELECT `+deviceColumns+` FROM mst_tv_devices WHERE device_uid = $1`, deviceUID)
	return scanDevice(row)
}

// GetByTokenHash resolves a device from the digest of its bearer token.
func (l *Local) GetByTokenHash(ctx context.Context, tokenHash string) (*domain.Device, error) {
	if tokenHash == "" {
		return nil, domain.WrapNotFound("device")
	}
	row := l.pool.QueryRow(ctx, `SELECT `+deviceColumns+` FROM mst_tv_devices WHERE device_token_hash = $1`, tokenHash)
	return scanDevice(row)
}

// GetByPairingCode resolves the device waiting for a pairing code.
func (l *Local) GetByPairingCode(ctx context.Context, code string) (*domain.Device, error) {
	if code == "" {
		return nil, domain.WrapNotFound("device")
	}
	row := l.pool.QueryRow(ctx, `
		SELECT `+deviceColumns+`
		FROM mst_tv_devices
		WHERE pairing_code = $1
		  AND (pairing_expires_at IS NULL OR pairing_expires_at > now())`, code)
	return scanDevice(row)
}

// ClaimDevice binds a physical TV to a pre-registered device row and stores
// the hashed credential. The pairing code is consumed in the same statement so
// it can never be reused.
func (l *Local) ClaimDevice(ctx context.Context, id, deviceUID, tokenHash string) error {
	tag, err := l.pool.Exec(ctx, `
		UPDATE mst_tv_devices
		SET device_uid = $2,
		    device_token_hash = $3,
		    pairing_code = NULL,
		    pairing_expires_at = NULL,
		    status = 'ONLINE',
		    last_seen_at = now()
		WHERE id = $1`, id, deviceUID, tokenHash)
	if err != nil {
		return mapWriteError("device", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.WrapNotFound("device")
	}
	return nil
}

// GetByID returns one device scoped to a tenant.
func (l *Local) GetDeviceByID(ctx context.Context, tenantID, id string) (*domain.Device, error) {
	row := l.pool.QueryRow(ctx, `SELECT `+deviceColumns+` FROM mst_tv_devices WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	return scanDevice(row)
}

// List returns devices matching the filter. An empty TenantID lists every
// device, which is the super admin fleet view.
func (l *Local) ListDevices(ctx context.Context, f domain.DeviceFilter) ([]domain.Device, int64, error) {
	limit, offset := paginate(f.Page, f.Limit)

	var (
		conditions []string
		args       []any
	)
	placeholder := func(value any) string {
		args = append(args, value)
		return "$" + itoa(len(args))
	}

	if f.TenantID != "" {
		conditions = append(conditions, "tenant_id = "+placeholder(f.TenantID))
	}
	if f.RoomID != "" {
		conditions = append(conditions, "room_id = "+placeholder(f.RoomID))
	}
	if f.Status != "" {
		conditions = append(conditions, "status = "+placeholder(f.Status))
	}
	if f.Search != "" {
		pattern := "%" + f.Search + "%"
		conditions = append(conditions,
			"(lower(device_uid) LIKE lower("+placeholder(pattern)+") OR lower(label) LIKE lower("+placeholder(pattern)+"))")
	}

	clause := ""
	if len(conditions) > 0 {
		clause = " WHERE " + strings.Join(conditions, " AND ")
	}

	var total int64
	if err := l.pool.QueryRow(ctx, `SELECT count(*) FROM mst_tv_devices`+clause, args...).Scan(&total); err != nil {
		return nil, 0, mapWriteError("device", err)
	}

	query := `SELECT ` + deviceColumns + ` FROM mst_tv_devices` + clause +
		` ORDER BY created_at DESC LIMIT ` + placeholder(limit) + ` OFFSET ` + placeholder(offset)

	rows, err := l.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, mapWriteError("device", err)
	}
	defer rows.Close()

	out := make([]domain.Device, 0, limit)
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *d)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, mapWriteError("device", err)
	}
	return out, total, nil
}

// ListByRoom returns every device installed in a cabin.
func (l *Local) ListByRoom(ctx context.Context, tenantID, roomID string) ([]domain.Device, error) {
	rows, err := l.pool.Query(ctx, `
		SELECT `+deviceColumns+`
		FROM mst_tv_devices
		WHERE tenant_id = $1 AND room_id = $2
		ORDER BY created_at`, tenantID, roomID)
	if err != nil {
		return nil, mapWriteError("device", err)
	}
	defer rows.Close()

	out := make([]domain.Device, 0, 4)
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	if err := rows.Err(); err != nil {
		return nil, mapWriteError("device", err)
	}
	return out, nil
}

// Create inserts a device.
func (l *Local) CreateDevice(ctx context.Context, d *domain.Device) error {
	if d.Status == "" {
		d.Status = domain.DeviceUnpaired
	}
	row := l.pool.QueryRow(ctx, `
		INSERT INTO mst_tv_devices (tenant_id, room_id, device_uid, label, model, app_version, pairing_code, pairing_expires_at, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING `+deviceColumns,
		d.TenantID, d.RoomID, d.DeviceUID, d.Label, d.Model, d.AppVersion, d.PairingCode, d.PairingExpiresAt, d.Status)
	created, err := scanDevice(row)
	if err != nil {
		return err
	}
	*d = *created
	return nil
}

// UpdateStatus sets the presence state and last known address.
func (l *Local) UpdateDeviceStatus(ctx context.Context, id string, status domain.DeviceStatus, ip string) error {
	_, err := l.pool.Exec(ctx, `
		UPDATE mst_tv_devices
		SET status = $2, ip_address = $3, last_seen_at = now()
		WHERE id = $1`, id, status, ip)
	return mapWriteError("device", err)
}

// SetPairingCode rotates the one-time code shown on the TV.
func (l *Local) SetPairingCode(ctx context.Context, id, code string, expiresAt time.Time) error {
	tag, err := l.pool.Exec(ctx, `
		UPDATE mst_tv_devices
		SET pairing_code = $2, pairing_expires_at = $3
		WHERE id = $1`, id, code, expiresAt)
	if err != nil {
		return mapWriteError("device", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.WrapNotFound("device")
	}
	return nil
}

// SetToken stores the hashed long lived device credential and activates the
// device. The pairing code is cleared so it cannot be reused.
func (l *Local) SetToken(ctx context.Context, id, tokenHash string) error {
	tag, err := l.pool.Exec(ctx, `
		UPDATE mst_tv_devices
		SET device_token_hash = $2, pairing_code = NULL, pairing_expires_at = NULL,
		    status = 'ONLINE', last_seen_at = now()
		WHERE id = $1`, id, tokenHash)
	if err != nil {
		return mapWriteError("device", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.WrapNotFound("device")
	}
	return nil
}

// TouchSeen records a heartbeat.
func (l *Local) TouchSeen(ctx context.Context, id, appVersion, ip string) error {
	_, err := l.pool.Exec(ctx, `
		UPDATE mst_tv_devices
		SET status = 'ONLINE', last_seen_at = now(),
		    app_version = COALESCE(NULLIF($2, ''), app_version),
		    ip_address = COALESCE(NULLIF($3, ''), ip_address)
		WHERE id = $1`, id, appVersion, ip)
	return mapWriteError("device", err)
}

// AssignRoom binds a device to a cabin.
func (l *Local) AssignRoom(ctx context.Context, tenantID, deviceID string, roomID *string) error {
	tag, err := l.pool.Exec(ctx, `
		UPDATE mst_tv_devices SET room_id = $3 WHERE tenant_id = $1 AND id = $2`,
		tenantID, deviceID, roomID)
	if err != nil {
		return mapWriteError("device", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.WrapNotFound("device")
	}
	return nil
}

// CountByTenant counts installed devices.
func (l *Local) CountByTenant(ctx context.Context, tenantID string) (int64, error) {
	var n int64
	err := l.pool.QueryRow(ctx, `SELECT count(*) FROM mst_tv_devices WHERE tenant_id = $1`, tenantID).Scan(&n)
	return n, mapWriteError("device", err)
}

// CountOnline counts devices seen within the heartbeat window.
func (l *Local) CountOnline(ctx context.Context, tenantID string) (int64, error) {
	var n int64
	err := l.pool.QueryRow(ctx, `
		SELECT count(*) FROM mst_tv_devices
		WHERE tenant_id = $1 AND status = 'ONLINE' AND last_seen_at > now() - interval '5 minutes'`, tenantID).Scan(&n)
	return n, mapWriteError("device", err)
}

// itoa is a tiny int to string helper that avoids importing strconv at each
// call site of the dynamic filter builder.
func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
