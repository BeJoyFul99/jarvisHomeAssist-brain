package sse

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
)

// Event types broadcast to connected clients.
const (
	EventUserCreated       = "user:created"
	EventUserUpdated       = "user:updated"
	EventUserDeleted       = "user:deleted"
	EventUserRestored      = "user:restored"
	EventUserLocked        = "user:locked"
	EventUserUnlocked      = "user:unlocked"
	EventUserTokensRevoked = "user:tokens_revoked"
	EventUserPinRegenerated = "user:pin_regenerated"
)

// Event is the payload sent over SSE.
type Event struct {
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}

// UserEventData carries details about which user was affected.
type UserEventData struct {
	UserID uint   `json:"user_id"`
	Email  string `json:"email"`
	Role   string `json:"role,omitempty"`
}

// client is an SSE subscriber channel.
type client struct {
	ch    chan []byte
	email string // the connected user's email (from JWT)
}

// Hub manages SSE client connections and broadcasts events.
type Hub struct {
	mu      sync.RWMutex
	clients map[*client]struct{}
}

// NewHub creates a new SSE hub.
func NewHub() *Hub {
	return &Hub{
		clients: make(map[*client]struct{}),
	}
}

// Subscribe adds a new client and returns its channel + unsubscribe func.
func (h *Hub) Subscribe(email string) (ch <-chan []byte, unsubscribe func()) {
	c := &client{
		ch:    make(chan []byte, 64),
		email: email,
	}
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()

	log.Printf("[sse] client connected: %s (total: %d)", email, h.Count())

	return c.ch, func() {
		h.mu.Lock()
		delete(h.clients, c)
		close(c.ch)
		h.mu.Unlock()
		log.Printf("[sse] client disconnected: %s (total: %d)", email, h.Count())
	}
}

// Count returns the number of connected clients.
func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// Broadcast sends an event to all connected clients.
func (h *Hub) Broadcast(event Event) {
	data, err := json.Marshal(event)
	if err != nil {
		log.Printf("[sse] marshal error: %v", err)
		return
	}
	msg := []byte(fmt.Sprintf("data: %s\n\n", data))

	h.mu.RLock()
	defer h.mu.RUnlock()

	for c := range h.clients {
		select {
		case c.ch <- msg:
		default:
			// client buffer full, skip
			log.Printf("[sse] dropping message for slow client: %s", c.email)
		}
	}
}

// BroadcastUserEvent is a convenience to broadcast a user-related event.
func (h *Hub) BroadcastUserEvent(eventType string, userID uint, email, role string) {
	h.Broadcast(Event{
		Type: eventType,
		Data: UserEventData{
			UserID: userID,
			Email:  email,
			Role:   role,
		},
	})
}
