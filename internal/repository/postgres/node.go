package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/alfateve/stayin-local-backend/internal/domain"
)

const nodeColumns = `id, tenant_id, node_code, name, base_url, tunnel_ip, status, agent_version, metrics, inventory, last_heartbeat_at, last_inventory_at, created_at, updated_at`

func scanNode(row pgx.Row) (*domain.EdgeNode, error) {
	var (
		n         domain.EdgeNode
		metrics   []byte
		inventory []byte
	)
	err := row.Scan(&n.ID, &n.TenantID, &n.NodeCode, &n.Name, &n.BaseURL, &n.TunnelIP, &n.Status,
		&n.AgentVersion, &metrics, &inventory, &n.LastHeartbeatAt, &n.LastInventoryAt,
		&n.CreatedAt, &n.UpdatedAt)
	if err != nil {
		return nil, mapWriteError("node", err)
	}
	n.Metrics = rawJSON(metrics)
	n.Inventory = rawJSON(inventory)
	return &n, nil
}

// List returns nodes. An empty tenantID lists every node.
func (s *Store) ListNodes(ctx context.Context, tenantID string, p domain.Pagination) ([]domain.EdgeNode, int64, error) {
	limit, offset := paginate(p.Page, p.Limit)

	var (
		total int64
		rows  pgx.Rows
		err   error
	)
	if tenantID == "" {
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM mst_edge_nodes`).Scan(&total); err != nil {
			return nil, 0, mapWriteError("node", err)
		}
		rows, err = s.pool.Query(ctx, `SELECT `+nodeColumns+` FROM mst_edge_nodes ORDER BY created_at DESC LIMIT $1 OFFSET $2`, limit, offset)
	} else {
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM mst_edge_nodes WHERE tenant_id = $1`, tenantID).Scan(&total); err != nil {
			return nil, 0, mapWriteError("node", err)
		}
		rows, err = s.pool.Query(ctx, `SELECT `+nodeColumns+` FROM mst_edge_nodes WHERE tenant_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`, tenantID, limit, offset)
	}
	if err != nil {
		return nil, 0, mapWriteError("node", err)
	}
	defer rows.Close()

	out := make([]domain.EdgeNode, 0, limit)
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *n)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, mapWriteError("node", err)
	}
	return out, total, nil
}

// GetByID returns one node scoped to a tenant.
func (s *Store) GetNodeByID(ctx context.Context, tenantID, id string) (*domain.EdgeNode, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+nodeColumns+` FROM mst_edge_nodes WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	return scanNode(row)
}

// GetByCode returns the node identified by its operational code.
func (s *Store) GetByCode(ctx context.Context, tenantID, code string) (*domain.EdgeNode, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+nodeColumns+` FROM mst_edge_nodes WHERE tenant_id = $1 AND node_code = $2`, tenantID, code)
	return scanNode(row)
}

// Upsert registers or refreshes a node row.
func (s *Store) UpsertNode(ctx context.Context, n *domain.EdgeNode) error {
	metrics, err := marshalJSON(n.Metrics)
	if err != nil {
		return err
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO mst_edge_nodes (tenant_id, node_code, name, base_url, tunnel_ip, status, agent_version, metrics, last_heartbeat_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, now())
		ON CONFLICT (tenant_id, node_code) DO UPDATE
		SET name = EXCLUDED.name,
		    base_url = EXCLUDED.base_url,
		    tunnel_ip = EXCLUDED.tunnel_ip,
		    status = EXCLUDED.status,
		    agent_version = EXCLUDED.agent_version,
		    metrics = EXCLUDED.metrics,
		    last_heartbeat_at = now()
		RETURNING `+nodeColumns,
		n.TenantID, n.NodeCode, n.Name, n.BaseURL, n.TunnelIP, n.Status, n.AgentVersion, metrics)
	saved, err := scanNode(row)
	if err != nil {
		return err
	}
	*n = *saved
	return nil
}

// UpdateMetrics stores a telemetry snapshot and marks the node online.
func (s *Store) UpdateMetrics(ctx context.Context, id string, metrics []byte, status string) error {
	payload, err := marshalJSON(metrics)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE mst_edge_nodes
		SET metrics = $2::jsonb, status = $3, last_heartbeat_at = now()
		WHERE id = $1`, id, payload, status)
	if err != nil {
		return mapWriteError("node", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.WrapNotFound("node")
	}
	return nil
}

// UpsertInventory stores the last mst_movie_local report from a node. The CMS
// reads it to show ship content state even while the node is unreachable.
func (s *Store) UpsertInventory(ctx context.Context, tenantID, nodeCode string, inventory []byte) error {
	payload, err := marshalJSON(inventory)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE mst_edge_nodes
		SET inventory = $3::jsonb, last_inventory_at = now(), status = 'ONLINE', last_heartbeat_at = now()
		WHERE tenant_id = $1 AND node_code = $2`, tenantID, nodeCode, payload)
	if err != nil {
		return mapWriteError("node", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.WrapNotFound("node")
	}
	return nil
}
