package domain

// Notifier delivers real-time events to connected clients. The websocket hub
// implements it; usecases depend only on this interface.
type Notifier interface {
	// SendToDevice delivers an event to one TV. It reports whether the device
	// had an active connection.
	SendToDevice(deviceID string, event string, payload any) bool
	// BroadcastTenant delivers an event to every connected TV of a tenant and
	// returns the number of sockets written.
	BroadcastTenant(tenantID string, event string, payload any) int
	// SendToRoom delivers an event to every TV installed in a cabin.
	SendToRoom(roomID string, event string, payload any) int
	// IsOnline reports whether the device currently holds a socket.
	IsOnline(deviceID string) bool
}

// NoopNotifier discards every event. Useful in tests and in cloud mode where
// no TV socket terminates on this process.
type NoopNotifier struct{}

// SendToDevice always reports a miss.
func (NoopNotifier) SendToDevice(string, string, any) bool { return false }

// BroadcastTenant writes nothing.
func (NoopNotifier) BroadcastTenant(string, string, any) int { return 0 }

// SendToRoom writes nothing.
func (NoopNotifier) SendToRoom(string, string, any) int { return 0 }

// IsOnline always reports false.
func (NoopNotifier) IsOnline(string) bool { return false }
