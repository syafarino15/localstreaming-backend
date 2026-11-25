package ws

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"

	"github.com/alfateve/stayin-local-backend/internal/domain"
	"github.com/alfateve/stayin-local-backend/internal/platform/auth"
)

// Handler upgrades HTTP requests into hub connections.
type Handler struct {
	Hub            *Hub
	Devices        domain.DeviceRepository
	Commands       domain.CommandRepository
	Log            zerolog.Logger
	PongWait       time.Duration
	AllowedOrigins []string
	upgrader       websocket.Upgrader
}

// NewHandler builds the websocket handler.
func NewHandler(
	hub *Hub,
	devices domain.DeviceRepository,
	commands domain.CommandRepository,
	log zerolog.Logger,
	pongWait time.Duration,
	allowedOrigins []string,
) *Handler {
	h := &Handler{
		Hub:            hub,
		Devices:        devices,
		Commands:       commands,
		Log:            log.With().Str("component", "ws-handler").Logger(),
		PongWait:       pongWait,
		AllowedOrigins: allowedOrigins,
	}
	h.upgrader = websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		CheckOrigin:     h.checkOrigin,
	}
	return h
}

// checkOrigin allows same host connections and the configured CMS origins.
// TV clients send no Origin header at all, which is always accepted.
func (h *Handler) checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	for _, allowed := range h.AllowedOrigins {
		if strings.EqualFold(strings.TrimSpace(allowed), origin) {
			return true
		}
	}
	if strings.Contains(origin, r.Host) {
		return true
	}
	return false
}

// TV handles a device socket at /ws/tv?token=...
func (h *Handler) TV(c *gin.Context) {
	token := c.Query("token")
	if token == "" {
		header := c.GetHeader("Authorization")
		if strings.HasPrefix(strings.ToLower(header), "bearer ") {
			token = strings.TrimSpace(header[7:])
		}
	}
	if token == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "token perangkat wajib diisi"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	device, err := h.Devices.GetByTokenHash(ctx, auth.HashToken(token))
	cancel()
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "token perangkat tidak valid"})
		return
	}

	conn, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		h.Log.Warn().Err(err).Msg("upgrade device socket failed")
		return
	}

	client := &Client{
		conn:     conn,
		send:     make(chan []byte, 32),
		DeviceID: device.ID,
		TenantID: device.TenantID,
		Kind:     KindTV,
		done:     make(chan struct{}),
	}
	if device.RoomID != nil {
		client.RoomID = *device.RoomID
	}

	h.Hub.register(client)
	go h.Hub.runWriter(client)

	h.Log.Info().
		Str("device_id", device.ID).
		Str("room", client.RoomID).
		Str("ip", c.ClientIP()).
		Msg("device connected")

	// Presence is written with a fresh context: the request context is already
	// cancelled once the upgrade completed.
	presenceCtx, presenceCancel := context.WithTimeout(context.Background(), 10*time.Second)
	_ = h.Devices.UpdateDeviceStatus(presenceCtx, device.ID, domain.DeviceOnline, c.ClientIP())
	presenceCancel()

	client.Send("WELCOME", map[string]any{
		"device_id":   device.ID,
		"tenant_id":   device.TenantID,
		"room_id":     client.RoomID,
		"server_time": time.Now().UTC(),
	})

	h.deliverPending(client)

	h.readDeviceLoop(c, client, device)

	h.Hub.unregister(client)
	client.close()

	offlineCtx, offlineCancel := context.WithTimeout(context.Background(), 10*time.Second)
	_ = h.Devices.UpdateDeviceStatus(offlineCtx, device.ID, domain.DeviceOffline, "")
	offlineCancel()

	h.Log.Info().Str("device_id", device.ID).Msg("device disconnected")
}

func (h *Handler) deliverPending(client *Client) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	pending, err := h.Commands.ListPendingForDevice(ctx, client.DeviceID)
	cancel()
	if err != nil {
		h.Log.Warn().Err(err).Msg("load pending commands failed")
		return
	}

	for _, cmd := range pending {
		payload := map[string]any{
			"id":      cmd.ID,
			"command": cmd.Command,
		}
		if len(cmd.Payload) > 0 {
			payload["data"] = cmd.Payload
		}
		if client.Send("COMMAND", payload) {
			ackCtx, ackCancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = h.Commands.MarkSent(ackCtx, cmd.ID)
			ackCancel()
		}
	}
}

func (h *Handler) readDeviceLoop(c *gin.Context, client *Client, device *domain.Device) {
	defer client.close()

	pongWait := h.PongWait
	if pongWait <= 0 {
		pongWait = 120 * time.Second
	}

	conn := client.conn
	conn.SetReadLimit(16 * 1024)
	_ = conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		_, body, err := conn.ReadMessage()
		if err != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(pongWait))

		var msg Message
		if err := json.Unmarshal(body, &msg); err != nil {
			continue
		}

		switch strings.ToUpper(msg.Type) {
		case "HEARTBEAT", "PING":
			if strings.EqualFold(msg.Type, "PING") {
				client.Send("PONG", nil)
			}
			appVersion, state := extractHeartbeat(msg.Payload)
			_ = state
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = h.Devices.TouchSeen(ctx, device.ID, appVersion, c.ClientIP())
			cancel()

		case "ACK":
			commandID := extractCommandID(msg.Payload)
			if commandID == "" {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = h.Commands.MarkAcked(ctx, commandID)
			cancel()
		}
	}
}

// Remote handles a passenger phone socket at /ws/remote?room_id=...&tenant_id=...
func (h *Handler) Remote(c *gin.Context) {
	roomID := strings.TrimSpace(c.Query("room_id"))
	tenantID := strings.TrimSpace(c.Query("tenant_id"))
	if roomID == "" || tenantID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "room_id dan tenant_id wajib diisi"})
		return
	}

	conn, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		h.Log.Warn().Err(err).Msg("upgrade remote socket failed")
		return
	}

	client := &Client{
		conn:     conn,
		send:     make(chan []byte, 16),
		TenantID: tenantID,
		RoomID:   roomID,
		Kind:     KindRemote,
		done:     make(chan struct{}),
	}

	h.Hub.register(client)
	go h.Hub.runWriter(client)
	defer func() {
		h.Hub.unregister(client)
		client.close()
	}()

	client.Send("WELCOME", map[string]any{"room_id": roomID, "role": "remote"})

	conn.SetReadLimit(4 * 1024)
	_ = conn.SetReadDeadline(time.Now().Add(h.PongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(h.PongWait))
	})

	for {
		_, body, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var msg Message
		if err := json.Unmarshal(body, &msg); err != nil {
			continue
		}

		switch strings.ToUpper(msg.Type) {
		case "KEY":
			key := extractKey(msg.Payload)
			if key == "" {
				continue
			}
			delivered := h.Hub.SendToRoom(roomID, "KEY", map[string]any{"key": key})
			client.Send("KEY_ACK", map[string]any{"key": key, "delivered": delivered})
		case "PING":
			client.Send("PONG", nil)
		}
	}
}

func extractHeartbeat(payload any) (appVersion string, state string) {
	m, ok := payload.(map[string]any)
	if !ok {
		return "", ""
	}
	if v, ok := m["app_version"].(string); ok {
		appVersion = v
	}
	if v, ok := m["state"].(string); ok {
		state = v
	}
	return appVersion, state
}

func extractCommandID(payload any) string {
	m, ok := payload.(map[string]any)
	if !ok {
		return ""
	}
	id, _ := m["command_id"].(string)
	if id == "" {
		id, _ = m["id"].(string)
	}
	return id
}

func extractKey(payload any) string {
	m, ok := payload.(map[string]any)
	if !ok {
		return ""
	}
	key, _ := m["key"].(string)
	return strings.ToUpper(strings.TrimSpace(key))
}
