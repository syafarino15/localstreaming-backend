package http

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/alfateve/stayin-local-backend/internal/domain"
	"github.com/alfateve/stayin-local-backend/internal/usecase"
	"github.com/alfateve/stayin-local-backend/pkg/response"
)

// NodeHandler receives reports from a local server. It is authenticated with a
// shared node key instead of an admin session, because the caller is another
// deployment of this product, not a human.
type NodeHandler struct {
	admin  *usecase.Admin
	apiKey string
}

// NewNodeHandler builds the node report handler. An empty apiKey disables the
// endpoint entirely rather than leaving it open.
func NewNodeHandler(admin *usecase.Admin, apiKey string) *NodeHandler {
	return &NodeHandler{admin: admin, apiKey: strings.TrimSpace(apiKey)}
}

// ReportInventory handles POST /api/v1/nodes/inventory.
func (h *NodeHandler) ReportInventory(c *gin.Context) {
	if h.apiKey == "" {
		response.Error(c, http.StatusServiceUnavailable, "pelaporan node belum dikonfigurasi")
		return
	}
	provided := strings.TrimSpace(c.GetHeader("X-Node-Key"))
	if subtle.ConstantTimeCompare([]byte(provided), []byte(h.apiKey)) != 1 {
		response.Unauthorized(c, "node key tidak valid")
		return
	}

	var body struct {
		TenantID string                  `json:"tenant_id" binding:"required"`
		NodeCode string                  `json:"node_code" binding:"required"`
		Items    []domain.InventoryEntry `json:"items"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		response.BadRequest(c, "payload inventory tidak valid",
			response.FieldError{Field: "body", Message: err.Error()})
		return
	}

	if err := h.admin.RecordInventory(c.Request.Context(), body.TenantID, body.NodeCode, body.Items); err != nil {
		MapError(c, err)
		return
	}
	response.OK(c, "inventory node tersimpan", gin.H{
		"node_code": body.NodeCode,
		"items":     len(body.Items),
	})
}
