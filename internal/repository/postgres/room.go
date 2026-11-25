package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/alfateve/stayin-local-backend/internal/domain"
)

const roomColumns = `id, tenant_id, room_number, deck, room_type, is_exclusive, guest_name, guest_salutation, guest_checkin_at, guest_checkout_at, status, created_at, updated_at`

func scanRoom(row pgx.Row) (*domain.Room, error) {
	var r domain.Room
	err := row.Scan(&r.ID, &r.TenantID, &r.RoomNumber, &r.Deck, &r.RoomType, &r.IsExclusive,
		&r.GuestName, &r.GuestSalutation, &r.GuestCheckinAt, &r.GuestCheckoutAt, &r.Status, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, mapWriteError("room", err)
	}
	return &r, nil
}

// List returns rooms for a tenant ordered by deck then room number.
func (l *Local) ListRooms(ctx context.Context, tenantID string, p domain.Pagination) ([]domain.Room, int64, error) {
	limit, offset := paginate(p.Page, p.Limit)

	var total int64
	if err := l.pool.QueryRow(ctx, `SELECT count(*) FROM mst_rooms WHERE tenant_id = $1`, tenantID).Scan(&total); err != nil {
		return nil, 0, mapWriteError("room", err)
	}

	rows, err := l.pool.Query(ctx, `
		SELECT `+roomColumns+`
		FROM mst_rooms
		WHERE tenant_id = $1
		ORDER BY deck, room_number
		LIMIT $2 OFFSET $3`, tenantID, limit, offset)
	if err != nil {
		return nil, 0, mapWriteError("room", err)
	}
	defer rows.Close()

	out := make([]domain.Room, 0, limit)
	for rows.Next() {
		r, err := scanRoom(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, mapWriteError("room", err)
	}
	return out, total, nil
}

// GetByID returns one room scoped to a tenant.
func (l *Local) GetRoomByID(ctx context.Context, tenantID, id string) (*domain.Room, error) {
	row := l.pool.QueryRow(ctx, `SELECT `+roomColumns+` FROM mst_rooms WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	return scanRoom(row)
}

// GetByNumber returns the room with the given cabin number.
func (l *Local) GetRoomByNumber(ctx context.Context, tenantID, number string) (*domain.Room, error) {
	row := l.pool.QueryRow(ctx, `SELECT `+roomColumns+` FROM mst_rooms WHERE tenant_id = $1 AND room_number = $2`, tenantID, number)
	return scanRoom(row)
}

// Create inserts a room.
func (l *Local) CreateRoom(ctx context.Context, r *domain.Room) error {
	if r.RoomType == "" {
		r.RoomType = "STANDARD"
	}
	if r.Status == "" {
		r.Status = "ACTIVE"
	}
	row := l.pool.QueryRow(ctx, `
		INSERT INTO mst_rooms (tenant_id, room_number, deck, room_type, is_exclusive, status)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+roomColumns,
		r.TenantID, r.RoomNumber, r.Deck, r.RoomType, r.IsExclusive, r.Status)
	created, err := scanRoom(row)
	if err != nil {
		return err
	}
	*r = *created
	return nil
}

// Update rewrites the mutable room fields.
func (l *Local) UpdateRoom(ctx context.Context, r *domain.Room) error {
	row := l.pool.QueryRow(ctx, `
		UPDATE mst_rooms
		SET room_number = $3, deck = $4, room_type = $5, is_exclusive = $6, status = $7
		WHERE tenant_id = $1 AND id = $2
		RETURNING `+roomColumns,
		r.TenantID, r.ID, r.RoomNumber, r.Deck, r.RoomType, r.IsExclusive, r.Status)
	updated, err := scanRoom(row)
	if err != nil {
		return err
	}
	*r = *updated
	return nil
}

// SetGuest stores the VIP guest personalisation for a room.
func (l *Local) SetGuest(ctx context.Context, r *domain.Room) error {
	row := l.pool.QueryRow(ctx, `
		UPDATE mst_rooms
		SET guest_name = $3, guest_salutation = $4, guest_checkin_at = $5, guest_checkout_at = $6
		WHERE tenant_id = $1 AND id = $2
		RETURNING `+roomColumns,
		r.TenantID, r.ID, r.GuestName, r.GuestSalutation, r.GuestCheckinAt, r.GuestCheckoutAt)
	updated, err := scanRoom(row)
	if err != nil {
		return err
	}
	*r = *updated
	return nil
}

// ClearGuest checks the guest out of a room.
func (l *Local) ClearGuest(ctx context.Context, tenantID, id string) error {
	tag, err := l.pool.Exec(ctx, `
		UPDATE mst_rooms
		SET guest_name = '', guest_salutation = '', guest_checkin_at = NULL, guest_checkout_at = NULL
		WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	if err != nil {
		return mapWriteError("room", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.WrapNotFound("room")
	}
	return nil
}

// Count returns the number of rooms for a tenant.
func (l *Local) CountRooms(ctx context.Context, tenantID string) (int64, error) {
	var n int64
	err := l.pool.QueryRow(ctx, `SELECT count(*) FROM mst_rooms WHERE tenant_id = $1`, tenantID).Scan(&n)
	return n, mapWriteError("room", err)
}
