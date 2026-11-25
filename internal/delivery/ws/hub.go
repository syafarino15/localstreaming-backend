// Package ws implements the real-time device hub. TVs hold a socket for
// heartbeat, remote commands and instant VIP guest personalisation. Passenger
// phones connect to the same hub and forward D-Pad keys to the TV of a cabin.
package ws

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"

	"github.com/alfateve/stayin-local-backend/internal/domain"
)

// Client kinds.
const (
	KindTV     = "tv"
	KindRemote = "remote"
)

// Message is the wire envelope for every websocket frame.
type Message struct {
	Type    string `json:"type"`
	Payload any    `json:"payload,omitempty"`
}

// Client is one connected socket.
type Client struct {
	conn     *websocket.Conn
	send     chan []byte
	DeviceID string
	TenantID string
	RoomID   string
	Kind     string

	closeOnce sync.Once
	done      chan struct{}
}

// Send queues a payload for delivery. A slow client is disconnected instead of
// blocking the hub, which protects the streaming path.
func (c *Client) Send(event string, payload any) bool {
	body, err := json.Marshal(Message{Type: event, Payload: payload})
	if err != nil {
		return false
	}
	select {
	case c.send <- body:
		return true
	case <-c.done:
		return false
	default:
		c.close()
		return false
	}
}

func (c *Client) close() {
	c.closeOnce.Do(func() {
		close(c.done)
		_ = c.conn.Close()
	})
}

// Hub tracks connected clients.
type Hub struct {
	mu       sync.RWMutex
	byDevice map[string]*Client
	byRoom   map[string]map[*Client]struct{}
	remotes  map[*Client]struct{}

	log          zerolog.Logger
	pingInterval time.Duration
	writeTimeout time.Duration
}

// NewHub builds the hub.
func NewHub(log zerolog.Logger, pingInterval, writeTimeout time.Duration) *Hub {
	if pingInterval <= 0 {
		pingInterval = 25 * time.Second
	}
	if writeTimeout <= 0 {
		writeTimeout = 10 * time.Second
	}
	return &Hub{
		byDevice:     map[string]*Client{},
		byRoom:       map[string]map[*Client]struct{}{},
		remotes:      map[*Client]struct{}{},
		log:          log.With().Str("component", "ws-hub").Logger(),
		pingInterval: pingInterval,
		writeTimeout: writeTimeout,
	}
}

// Count returns the number of connected TVs.
func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.byDevice)
}

func (h *Hub) register(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if c.Kind == KindTV {
		// A reconnecting TV replaces its previous socket.
		if old, ok := h.byDevice[c.DeviceID]; ok && old != c {
			old.close()
		}
		h.byDevice[c.DeviceID] = c
		if c.RoomID != "" {
			if h.byRoom[c.RoomID] == nil {
				h.byRoom[c.RoomID] = map[*Client]struct{}{}
			}
			h.byRoom[c.RoomID][c] = struct{}{}
		}
		return
	}

	h.remotes[c] = struct{}{}
}

func (h *Hub) unregister(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if c.Kind == KindTV {
		if current, ok := h.byDevice[c.DeviceID]; ok && current == c {
			delete(h.byDevice, c.DeviceID)
		}
		if c.RoomID != "" {
			if set, ok := h.byRoom[c.RoomID]; ok {
				delete(set, c)
				if len(set) == 0 {
					delete(h.byRoom, c.RoomID)
				}
			}
		}
		return
	}
	delete(h.remotes, c)
}

// SendToDevice delivers an event to one TV.
func (h *Hub) SendToDevice(deviceID string, event string, payload any) bool {
	h.mu.RLock()
	client, ok := h.byDevice[deviceID]
	h.mu.RUnlock()
	if !ok {
		return false
	}
	return client.Send(event, payload)
}

// BroadcastTenant delivers an event to every TV of a tenant.
func (h *Hub) BroadcastTenant(tenantID string, event string, payload any) int {
	h.mu.RLock()
	targets := make([]*Client, 0, len(h.byDevice))
	for _, c := range h.byDevice {
		if c.TenantID == tenantID {
			targets = append(targets, c)
		}
	}
	h.mu.RUnlock()

	delivered := 0
	for _, c := range targets {
		if c.Send(event, payload) {
			delivered++
		}
	}
	return delivered
}

// SendToRoom delivers an event to every TV in a cabin.
func (h *Hub) SendToRoom(roomID string, event string, payload any) int {
	h.mu.RLock()
	set := h.byRoom[roomID]
	targets := make([]*Client, 0, len(set))
	for c := range set {
		targets = append(targets, c)
	}
	h.mu.RUnlock()

	delivered := 0
	for _, c := range targets {
		if c.Send(event, payload) {
			delivered++
		}
	}
	return delivered
}

// IsOnline reports whether the device holds a socket.
func (h *Hub) IsOnline(deviceID string) bool {
	h.mu.RLock()
	_, ok := h.byDevice[deviceID]
	h.mu.RUnlock()
	return ok
}

// OnlineDeviceIDs lists connected devices for one tenant.
func (h *Hub) OnlineDeviceIDs(tenantID string) []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]string, 0, len(h.byDevice))
	for id, c := range h.byDevice {
		if c.TenantID == tenantID {
			out = append(out, id)
		}
	}
	return out
}

// runWriter serialises writes and keeps the connection alive with pings.
func (h *Hub) runWriter(c *Client) {
	ticker := time.NewTicker(h.pingInterval)
	defer ticker.Stop()
	defer c.close()

	for {
		select {
		case body := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(h.writeTimeout))
			if err := c.conn.WriteMessage(websocket.TextMessage, body); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(h.writeTimeout))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-c.done:
			return
		}
	}
}

// Notifier compliance check.
var _ domain.Notifier = (*Hub)(nil)
