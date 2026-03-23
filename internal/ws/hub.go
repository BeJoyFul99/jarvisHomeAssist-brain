package ws

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = (pongWait * 9) / 10
	maxMsgSize = 4096
)

// Message is the envelope sent/received over WebSocket.
type Message struct {
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}

// Client represents a connected WebSocket user.
type Client struct {
	Hub    *Hub
	Conn   *websocket.Conn
	Send   chan []byte
	UserID uint
	Email  string
	Name   string
	// RoomIDs the client has access to (set on connect, updated on room changes)
	RoomIDs map[uint]struct{}
	mu      sync.RWMutex
}

// Hub manages WebSocket client connections.
type Hub struct {
	mu         sync.RWMutex
	clients    map[*Client]struct{}
	Register   chan *Client
	Unregister chan *Client
}

// NewHub creates a new WebSocket hub and starts the run loop.
func NewHub() *Hub {
	h := &Hub{
		clients:    make(map[*Client]struct{}),
		Register:   make(chan *Client),
		Unregister: make(chan *Client),
	}
	go h.run()
	return h
}

func (h *Hub) run() {
	for {
		select {
		case client := <-h.Register:
			h.mu.Lock()
			h.clients[client] = struct{}{}
			h.mu.Unlock()
			log.Printf("[ws] client connected: %s (total: %d)", client.Email, h.Count())

		case client := <-h.Unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.Send)
			}
			h.mu.Unlock()
			log.Printf("[ws] client disconnected: %s (total: %d)", client.Email, h.Count())
		}
	}
}

// Count returns the number of connected clients.
func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// BroadcastToRoom sends a message to all clients that have access to the given room.
func (h *Hub) BroadcastToRoom(roomID uint, msg Message) {
	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("[ws] marshal error: %v", err)
		return
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	for c := range h.clients {
		c.mu.RLock()
		_, hasRoom := c.RoomIDs[roomID]
		c.mu.RUnlock()
		if hasRoom {
			select {
			case c.Send <- data:
			default:
				log.Printf("[ws] dropping message for slow client: %s", c.Email)
			}
		}
	}
}

// BroadcastToAll sends a message to all connected clients.
func (h *Hub) BroadcastToAll(msg Message) {
	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("[ws] marshal error: %v", err)
		return
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	for c := range h.clients {
		select {
		case c.Send <- data:
		default:
			log.Printf("[ws] dropping message for slow client: %s", c.Email)
		}
	}
}

// SendToUser sends a message to a specific user by ID.
func (h *Hub) SendToUser(userID uint, msg Message) {
	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("[ws] marshal error: %v", err)
		return
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	for c := range h.clients {
		if c.UserID == userID {
			select {
			case c.Send <- data:
			default:
			}
		}
	}
}

// AddRoomToClient grants a client access to a room for broadcast filtering.
func (h *Hub) AddRoomToClient(client *Client, roomID uint) {
	client.mu.Lock()
	client.RoomIDs[roomID] = struct{}{}
	client.mu.Unlock()
}

// ReadPump pumps messages from the WebSocket connection to the hub.
// The application must run this in a goroutine for each client.
func (c *Client) ReadPump(onMessage func(client *Client, msg Message)) {
	defer func() {
		c.Hub.Unregister <- c
		c.Conn.Close()
	}()

	c.Conn.SetReadLimit(maxMsgSize)
	c.Conn.SetReadDeadline(time.Now().Add(pongWait))
	c.Conn.SetPongHandler(func(string) error {
		c.Conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, raw, err := c.Conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("[ws] read error from %s: %v", c.Email, err)
			}
			break
		}

		var msg Message
		if err := json.Unmarshal(raw, &msg); err != nil {
			log.Printf("[ws] invalid message from %s: %v", c.Email, err)
			continue
		}

		if onMessage != nil {
			onMessage(c, msg)
		}
	}
}

// WritePump pumps messages from the Send channel to the WebSocket connection.
// The application must run this in a goroutine for each client.
func (c *Client) WritePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.Conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.Send:
			c.Conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				c.Conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := c.Conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(message)

			// Drain queued messages into same write
			n := len(c.Send)
			for i := 0; i < n; i++ {
				w.Write([]byte("\n"))
				w.Write(<-c.Send)
			}

			if err := w.Close(); err != nil {
				return
			}

		case <-ticker.C:
			c.Conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
