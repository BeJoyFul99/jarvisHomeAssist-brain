package handlers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"gorm.io/gorm"

	"jarvishomeassist-brain/internal/config"
	"jarvishomeassist-brain/internal/models"
	"jarvishomeassist-brain/internal/ws"
)

// Jarvis system prompt — defines the AI persona.
const jarvisSystemPrompt = `You are Jarvis, the AI assistant for a smart home system called Jarvis Home Assist. You help the family with home automation, answer questions, and provide useful information.

Personality:
- Friendly, warm, and concise
- Address family members by name when you know it
- You have awareness of smart home devices, energy usage, and network status
- Keep responses brief unless asked for detail
- If asked about something you don't know, say so honestly

In group chats, you only respond when mentioned with @jarvis. In direct conversations, you always respond.`

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		// Behind Cloudflare Tunnel the Origin header is the public domain.
		// Allow all origins — auth is enforced via JWT token in query param.
		return true
	},
}

// ChatHandler manages chat rooms, messages, and AI integration.
type ChatHandler struct {
	DB    *gorm.DB
	WSHub *ws.Hub
	Cfg   *config.Config
	// Semaphore to limit concurrent AI requests
	aiSem chan struct{}
	once  sync.Once
	// Short-lived single-use tickets for websocket handshake
	tickets   map[string]ticketEntry
	ticketsMu sync.Mutex
}

type ticketEntry struct {
	Email     string
	ExpiresAt time.Time
}

func (h *ChatHandler) initSem() {
	h.once.Do(func() {
		h.aiSem = make(chan struct{}, 3)
	})
}

// IssueTicket creates a short-lived single-use ticket for the given email.
func (h *ChatHandler) IssueTicket(email string, ttl time.Duration) string {
	ticket := uuid.New().String()
	h.ticketsMu.Lock()
	if h.tickets == nil {
		h.tickets = make(map[string]ticketEntry)
	}
	h.tickets[ticket] = ticketEntry{Email: email, ExpiresAt: time.Now().Add(ttl)}
	h.ticketsMu.Unlock()
	return ticket
}

// validateAndConsumeTicket checks a ticket and consumes it if valid.
func (h *ChatHandler) validateAndConsumeTicket(ticket string) (string, bool) {
	h.ticketsMu.Lock()
	defer h.ticketsMu.Unlock()
	if h.tickets == nil {
		return "", false
	}
	entry, ok := h.tickets[ticket]
	if !ok {
		return "", false
	}
	if time.Now().After(entry.ExpiresAt) {
		delete(h.tickets, ticket)
		return "", false
	}
	// single-use: delete now
	delete(h.tickets, ticket)
	return entry.Email, true
}

// SeedDefaultChatRooms creates the family group chat room if it doesn't exist.
func SeedDefaultChatRooms(db *gorm.DB) {
	var room models.ChatRoom
	if db.Where("type = ?", "group").First(&room).Error != nil {
		db.Create(&models.ChatRoom{
			Name: "Family Chat",
			Type: "group",
		})
		log.Println("[seed] created Family Chat room")
	}
}

// ── Helpers ────────────────────────────────────────────────────

// currentUser looks up the authenticated user from JWT context claims.
func (h *ChatHandler) currentUser(c *gin.Context) (*models.User, error) {
	email, _ := c.Get("user_email")
	emailStr, _ := email.(string)

	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	var user models.User
	if err := h.DB.WithContext(ctx).Where("email = ?", emailStr).First(&user).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

// ensureDirectAIRoom returns the user's direct_ai room, creating it if needed.
func (h *ChatHandler) ensureDirectAIRoom(userID uint, userName string) (*models.ChatRoom, error) {
	var room models.ChatRoom
	err := h.DB.Where("type = ? AND owner_id = ?", "direct_ai", userID).First(&room).Error
	if err == nil {
		return &room, nil
	}
	room = models.ChatRoom{
		Name:    "Jarvis AI",
		Type:    "direct_ai",
		OwnerID: &userID,
	}
	if err := h.DB.Create(&room).Error; err != nil {
		return nil, err
	}
	log.Printf("[chat] created direct AI room for %s (user %d)", userName, userID)
	return &room, nil
}

// canAccessRoom checks if the user can access a given room.
func (h *ChatHandler) canAccessRoom(room *models.ChatRoom, userID uint) bool {
	if room.Type == "group" {
		return true // all authenticated (non-guest) users can access group rooms
	}
	return room.OwnerID != nil && *room.OwnerID == userID
}

// ── REST Endpoints ─────────────────────────────────────────────

// ListRooms returns all rooms the user has access to.
// GET /api/v1/chat/rooms
func (h *ChatHandler) ListRooms(c *gin.Context) {
	user, err := h.currentUser(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user_not_found"})
		return
	}

	// Guests are not allowed to use chat
	if user.Role == models.RoleGuest {
		c.JSON(http.StatusForbidden, gin.H{"error": "access_denied", "message": "guests cannot access chat"})
		return
	}

	// Ensure the user has a direct AI room
	directRoom, err := h.ensureDirectAIRoom(user.ID, user.DisplayName)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create AI room"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	// Get the group room(s)
	var rooms []models.ChatRoom
	h.DB.WithContext(ctx).Where("type = ?", "group").Find(&rooms)

	// Add the direct AI room
	rooms = append(rooms, *directRoom)

	// Compute unread counts
	type roomResponse struct {
		models.ChatRoom
		UnreadCount int `json:"unread_count"`
	}

	result := make([]roomResponse, 0, len(rooms))
	for _, room := range rooms {
		unread := 0
		var receipt models.ChatReadReceipt
		if h.DB.WithContext(ctx).Where("room_id = ? AND user_id = ?", room.ID, user.ID).First(&receipt).Error == nil {
			h.DB.WithContext(ctx).Model(&models.ChatMessage{}).
				Where("room_id = ? AND id > ?", room.ID, receipt.LastRead).
				Count(new(int64))
			var count int64
			h.DB.WithContext(ctx).Model(&models.ChatMessage{}).
				Where("room_id = ? AND id > ?", room.ID, receipt.LastRead).
				Count(&count)
			unread = int(count)
		} else {
			// No receipt = all messages are unread
			var count int64
			h.DB.WithContext(ctx).Model(&models.ChatMessage{}).Where("room_id = ?", room.ID).Count(&count)
			unread = int(count)
		}

		result = append(result, roomResponse{
			ChatRoom:    room,
			UnreadCount: unread,
		})
	}

	c.JSON(http.StatusOK, result)
}

// createRoomRequest is the JSON body for creating a new chat room.
type createRoomRequest struct {
	Name         string `json:"name" binding:"required"`
	Type         string `json:"type" binding:"required"` // "group" or "direct_ai"
	Participants []uint `json:"participants"`            // optional participant IDs for group rooms
}

// CreateRoom creates a new chat room.
// POST /api/v1/chat/rooms
func (h *ChatHandler) CreateRoom(c *gin.Context) {
	user, err := h.currentUser(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user_not_found"})
		return
	}

	// Guests cannot create rooms
	if user.Role == models.RoleGuest {
		c.JSON(http.StatusForbidden, gin.H{"error": "access_denied", "message": "guests cannot create rooms"})
		return
	}

	var req createRoomRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Validate room type
	if req.Type != "group" && req.Type != "direct_ai" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_room_type", "message": "type must be 'group' or 'direct_ai'"})
		return
	}

	// Only the creator can have a direct_ai room; group rooms have no owner
	var ownerID *uint
	if req.Type == "direct_ai" {
		ownerID = &user.ID
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	// Create the room
	room := models.ChatRoom{
		Name:    req.Name,
		Type:    req.Type,
		OwnerID: ownerID,
	}

	if err := h.DB.WithContext(ctx).Create(&room).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create room"})
		return
	}

	log.Printf("[chat] created %s room '%s' (id: %d) by user %s", req.Type, req.Name, room.ID, user.DisplayName)

	// For group rooms, the participants array is noted but not stored (all authenticated users can access group rooms)
	// If in the future you want to use a ChatMembership table or participant restrictions, add that logic here.

	c.JSON(http.StatusOK, room)
}

// GetMessages returns messages for a room with pagination.
// GET /api/v1/chat/rooms/:id/messages?limit=50&before=<id>
func (h *ChatHandler) GetMessages(c *gin.Context) {
	user, err := h.currentUser(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user_not_found"})
		return
	}

	roomID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_room_id"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	var room models.ChatRoom
	if err := h.DB.WithContext(ctx).First(&room, roomID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "room_not_found"})
		return
	}

	if !h.canAccessRoom(&room, user.ID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "access_denied"})
		return
	}

	limit := 50
	if l, err := strconv.Atoi(c.Query("limit")); err == nil && l > 0 && l <= 200 {
		limit = l
	}

	query := h.DB.WithContext(ctx).Where("room_id = ?", roomID)

	if before, err := strconv.ParseUint(c.Query("before"), 10, 64); err == nil {
		query = query.Where("id < ?", before)
	}

	var messages []models.ChatMessage
	query.Order("id DESC").Limit(limit).Preload("ReplyTo").Find(&messages)

	// Reverse to chronological order
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}

	c.JSON(http.StatusOK, messages)
}

// sendMessageRequest is the JSON body for sending a message.
type sendMessageRequest struct {
	Content   string `json:"content" binding:"required"`
	Type      string `json:"type"`
	ReplyToID *uint  `json:"reply_to_id"`
}

// SendMessage saves a user message and optionally triggers AI.
// POST /api/v1/chat/rooms/:id/messages
// Accepts JSON body or multipart form data (for image uploads).
func (h *ChatHandler) SendMessage(c *gin.Context) {
	user, err := h.currentUser(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user_not_found"})
		return
	}

	roomID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_room_id"})
		return
	}

	// Parse request — handle both JSON and multipart form data
	var content, msgType string
	var replyToID *uint
	var mediaURL *string

	ct := c.ContentType()
	if strings.Contains(ct, "multipart/form-data") {
		content = c.PostForm("content")
		msgType = c.PostForm("type")
		if rid := c.PostForm("reply_to_id"); rid != "" {
			if v, err := strconv.ParseUint(rid, 10, 64); err == nil {
				uid := uint(v)
				replyToID = &uid
			}
		}

		// Handle file upload
		file, header, err := c.Request.FormFile("media")
		if err == nil {
			defer file.Close()

			// Validate: must be an image, max 10MB
			if header.Size > 10*1024*1024 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "file_too_large", "message": "Max 10MB"})
				return
			}
			fileCT := header.Header.Get("Content-Type")
			if !strings.HasPrefix(fileCT, "image/") {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_file_type", "message": "Only images allowed"})
				return
			}

			// Save file
			ext := filepath.Ext(header.Filename)
			if ext == "" {
				ext = ".jpg"
			}
			filename := fmt.Sprintf("%s%s", uuid.New().String(), ext)
			uploadDir := filepath.Join(h.Cfg.UploadBaseDir, h.Cfg.UploadChatSubdir)
			os.MkdirAll(uploadDir, 0755)
			savePath := filepath.Join(uploadDir, filename)

			out, err := os.Create(savePath)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save file"})
				return
			}
			defer out.Close()
			if _, err := io.Copy(out, file); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save file"})
				return
			}

			url := "/uploads/chat/" + filename
			mediaURL = &url
			if msgType == "" {
				msgType = "image"
			}
		}
	} else {
		var req sendMessageRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		content = req.Content
		msgType = req.Type
		replyToID = req.ReplyToID
	}

	if msgType == "" {
		msgType = "text"
	}
	if content == "" && mediaURL == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "content or media required"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	var room models.ChatRoom
	if err := h.DB.WithContext(ctx).First(&room, roomID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "room_not_found"})
		return
	}

	if !h.canAccessRoom(&room, user.ID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "access_denied"})
		return
	}

	// Save the message
	msg := models.ChatMessage{
		RoomID:     uint(roomID),
		SenderID:   &user.ID,
		SenderName: user.DisplayName,
		Role:       "user",
		Status:     "sent",
		Content:    content,
		Type:       msgType,
		MediaURL:   mediaURL,
		ReplyToID:  replyToID,
	}

	if err := h.DB.WithContext(ctx).Create(&msg).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save message"})
		return
	}

	// Update room's last message preview (non-blocking)
	preview := content
	if msgType == "image" {
		preview = "📷 Photo"
	}
	if len(preview) > 100 {
		preview = preview[:100] + "..."
	}
	go func() {
		now := time.Now()
		h.DB.Model(&models.ChatRoom{}).Where("id = ?", roomID).Updates(map[string]interface{}{
			"last_msg_text": preview,
			"last_msg_at":   now,
			"last_msg_by":   user.DisplayName,
		})
	}()

	// Broadcast the message via WebSocket
	h.WSHub.BroadcastToRoom(uint(roomID), ws.Message{
		Type: "chat:message",
		Data: msg,
	})

	// Determine if AI should respond
	shouldAIRespond := false
	if room.Type == "direct_ai" {
		shouldAIRespond = true
	} else if room.Type == "group" {
		lower := strings.ToLower(content)
		if strings.Contains(lower, "@jarvis") || strings.Contains(lower, "@ai") {
			shouldAIRespond = true
		}
		// Also respond if the user is replying to Jarvis's message
		if !shouldAIRespond && replyToID != nil {
			var repliedMsg models.ChatMessage
			if h.DB.First(&repliedMsg, *replyToID).Error == nil {
				if repliedMsg.Role == "assistant" {
					shouldAIRespond = true
				}
			}
		}
	}

	if shouldAIRespond && h.Cfg.CFWorkerURL != "" {
		go h.triggerAIResponse(uint(roomID), msg.ID)
	}

	c.JSON(http.StatusCreated, msg)
}

// MarkSeen updates the read receipt for a user in a room.
// POST /api/v1/chat/rooms/:id/seen
func (h *ChatHandler) MarkSeen(c *gin.Context) {
	user, err := h.currentUser(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user_not_found"})
		return
	}

	roomID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_room_id"})
		return
	}

	var body struct {
		LastRead uint `json:"last_read" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	receipt := models.ChatReadReceipt{
		RoomID:   uint(roomID),
		UserID:   user.ID,
		LastRead: body.LastRead,
	}

	// Upsert: update if exists, create if not
	result := h.DB.WithContext(ctx).
		Where("room_id = ? AND user_id = ?", roomID, user.ID).
		Assign(models.ChatReadReceipt{LastRead: body.LastRead}).
		FirstOrCreate(&receipt)

	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update read receipt"})
		return
	}

	// If it was an update (not create), we need to explicitly save
	if result.RowsAffected == 0 {
		h.DB.WithContext(ctx).Model(&receipt).Update("last_read", body.LastRead)
	}

	// Broadcast seen event
	h.WSHub.BroadcastToRoom(uint(roomID), ws.Message{
		Type: "chat:seen",
		Data: map[string]interface{}{
			"room_id":   roomID,
			"user_id":   user.ID,
			"user_name": user.DisplayName,
			"last_read": body.LastRead,
		},
	})

	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ListUsers returns a list of active family members for chat participant selection.
// GET /api/v1/chat/users
func (h *ChatHandler) ListUsers(c *gin.Context) {
	user, err := h.currentUser(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user_not_found"})
		return
	}

	// Guests cannot see user lists
	if user.Role == models.RoleGuest {
		c.JSON(http.StatusForbidden, gin.H{"error": "access_denied"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	// Get all non-guest, non-deleted users
	var users []models.User
	h.DB.WithContext(ctx).
		Where("role != ? AND deleted_at IS NULL", models.RoleGuest).
		Order("display_name ASC").
		Find(&users)

	type userItem struct {
		ID          uint   `json:"id"`
		Email       string `json:"email"`
		DisplayName string `json:"display_name"`
	}

	result := make([]userItem, 0, len(users))
	for _, u := range users {
		result = append(result, userItem{
			ID:          u.ID,
			Email:       u.Email,
			DisplayName: u.DisplayName,
		})
	}

	c.JSON(http.StatusOK, result)
}

// Authorize issues a short-lived single-use ticket for websocket handshake.
// POST /api/v1/chat/authorize
func (h *ChatHandler) Authorize(c *gin.Context) {
	user, err := h.currentUser(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user_not_found"})
		return
	}

	// Guests cannot get tickets
	if user.Role == models.RoleGuest {
		c.JSON(http.StatusForbidden, gin.H{"error": "access_denied", "message": "guests cannot access chat"})
		return
	}

	// Issue single-use ticket (30s TTL)
	ticket := h.IssueTicket(user.Email, 30*time.Second)

	c.JSON(http.StatusOK, gin.H{"ticket": ticket, "expires_in_seconds": 30})
}

// ── WebSocket Endpoint ─────────────────────────────────────────

// WebSocket upgrades the HTTP connection to WebSocket for real-time chat.
// GET /api/v1/chat/ws?token=<jwt>
// Auth is handled via query param since browsers can't send headers on WS upgrade.
func (h *ChatHandler) WebSocket(c *gin.Context) {
	// Support short-lived single-use tickets for WebSocket handshake.
	// Clients should POST /api/v1/chat/authorize with Authorization: Bearer <JWT>
	// to receive a ticket, then open WS with ?ticket=<uuid>.
	// Only support short-lived single-use tickets for WebSocket handshake.
	var user models.User
	ticket := c.Query("ticket")
	if ticket == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing_ticket"})
		return
	}
	email, ok := h.validateAndConsumeTicket(ticket)
	if !ok || email == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid_ticket"})
		return
	}
	if err := h.DB.Where("email = ?", email).First(&user).Error; err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user_not_found"})
		return
	}

	// Guests are not allowed to open chat websockets
	if user.Role == models.RoleGuest {
		c.JSON(http.StatusForbidden, gin.H{"error": "access_denied", "message": "guests cannot access chat"})
		return
	}

	// Set response headers that help reverse proxies (cloudflared, nginx) keep the connection alive
	upgradeHeaders := http.Header{}
	upgradeHeaders.Set("X-Accel-Buffering", "no") // disable proxy buffering

	conn, err := wsUpgrader.Upgrade(c.Writer, c.Request, upgradeHeaders)
	if err != nil {
		log.Printf("[chat-ws] upgrade error: %v", err)
		return
	}

	// Build the set of room IDs this user can access
	roomIDs := make(map[uint]struct{})

	// All users have access to group rooms
	var groupRooms []models.ChatRoom
	h.DB.Where("type = ?", "group").Find(&groupRooms)
	for _, r := range groupRooms {
		roomIDs[r.ID] = struct{}{}
	}

	// User's own direct AI room
	directRoom, _ := h.ensureDirectAIRoom(user.ID, user.DisplayName)
	if directRoom != nil {
		roomIDs[directRoom.ID] = struct{}{}
	}

	wsClient := &ws.Client{
		Hub:     h.WSHub,
		Conn:    conn,
		Send:    make(chan []byte, 256),
		UserID:  user.ID,
		Email:   user.Email,
		Name:    user.DisplayName,
		RoomIDs: roomIDs,
	}

	h.WSHub.Register <- wsClient

	go wsClient.WritePump()
	wsClient.ReadPump(h.handleWSMessage) // blocks until connection closes — required for reverse proxies like cloudflared
}

// handleWSMessage processes incoming WebSocket messages from clients.
func (h *ChatHandler) handleWSMessage(client *ws.Client, msg ws.Message) {
	switch msg.Type {
	case "typing":
		// Broadcast typing indicator to the room
		data, ok := msg.Data.(map[string]interface{})
		if !ok {
			return
		}
		roomIDFloat, ok := data["room_id"].(float64)
		if !ok {
			return
		}
		roomID := uint(roomIDFloat)
		isTyping, _ := data["is_typing"].(bool)

		h.WSHub.BroadcastToRoom(roomID, ws.Message{
			Type: "chat:typing",
			Data: map[string]interface{}{
				"room_id":   roomID,
				"user_id":   client.UserID,
				"user_name": client.Name,
				"is_typing": isTyping,
			},
		})
	}
}

// ── AI Response Flow ───────────────────────────────────────────

// triggerAIResponse calls the Cloudflare Worker to generate an AI response.
func (h *ChatHandler) triggerAIResponse(roomID uint, triggerMsgID uint) {
	h.initSem()

	// Acquire semaphore (limit concurrent AI requests)
	h.aiSem <- struct{}{}
	defer func() { <-h.aiSem }()

	// Broadcast "thinking" status
	h.WSHub.BroadcastToRoom(roomID, ws.Message{
		Type: "chat:ai_status",
		Data: map[string]interface{}{
			"room_id": roomID,
			"status":  "thinking",
		},
	})

	// Mark triggering message as processed
	h.DB.Model(&models.ChatMessage{}).Where("id = ?", triggerMsgID).Update("status", "processed")

	// Load conversation history (last 100 messages) with reply context
	var history []models.ChatMessage
	h.DB.Where("room_id = ?", roomID).Order("id DESC").Limit(100).Preload("ReplyTo").Find(&history)
	// Reverse to chronological order
	for i, j := 0, len(history)-1; i < j; i, j = i+1, j-1 {
		history[i], history[j] = history[j], history[i]
	}

	// Build messages array for the AI (supports multimodal content for vision model)
	type aiMsg struct {
		Role    string      `json:"role"`
		Content interface{} `json:"content"` // string or []contentPart
	}
	type contentPart struct {
		Type     string      `json:"type"`
		Text     string      `json:"text,omitempty"`
		ImageURL interface{} `json:"image_url,omitempty"`
	}
	type imageURLObj struct {
		URL string `json:"url"`
	}

	messages := make([]aiMsg, 0, len(history)+1)
	messages = append(messages, aiMsg{Role: "system", Content: jarvisSystemPrompt})
	for _, m := range history {
		role := m.Role
		if role == "system" {
			continue // skip system messages in history
		}
		content := m.Content
		if role == "user" {
			// Prefix with sender name for context
			content = m.SenderName + ": " + content
			// Include reply context so the AI knows what message is being replied to
			if m.ReplyTo != nil {
				replyPrefix := "[replying to " + m.ReplyTo.SenderName + ": \"" + m.ReplyTo.Content + "\"] "
				content = m.SenderName + ": " + replyPrefix + m.Content
			}
		}

		// If this is an image message with a local file, include as base64 for the vision model
		if m.Type == "image" && m.MediaURL != nil && role == "user" {
			imgData := readImageAsBase64(*m.MediaURL)
			if imgData != "" {
				parts := []contentPart{
					{Type: "text", Text: content},
					{Type: "image_url", ImageURL: imageURLObj{URL: imgData}},
				}
				messages = append(messages, aiMsg{Role: role, Content: parts})
				continue
			}
		}
		messages = append(messages, aiMsg{Role: role, Content: content})
	}

	// Call Cloudflare Worker
	reqBody, _ := json.Marshal(map[string]interface{}{
		"messages": messages,
		"stream":   true,
	})

	// Broadcast "responding" status
	h.WSHub.BroadcastToRoom(roomID, ws.Message{
		Type: "chat:ai_status",
		Data: map[string]interface{}{
			"room_id": roomID,
			"status":  "responding",
		},
	})

	client := &http.Client{Timeout: 120 * time.Second}
	req, err := http.NewRequest("POST", h.Cfg.CFWorkerURL+"/v1/chat", bytes.NewReader(reqBody))
	if err != nil {
		h.saveAIError(roomID, "Sorry, I'm having trouble connecting right now.")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+h.Cfg.CFWorkerSecret)

	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[chat-ai] request error: %v", err)
		h.saveAIError(roomID, "Sorry, I'm having trouble connecting right now.")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("[chat-ai] non-200 response (%d): %s", resp.StatusCode, string(body))
		h.saveAIError(roomID, "Sorry, I couldn't process that request right now.")
		return
	}

	// Read SSE stream from CF Worker
	var fullResponse strings.Builder
	scanner := bufio.NewScanner(resp.Body)

	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		if data == "[DONE]" {
			break
		}

		// CF Workers AI sends: data: {"response":"token"}
		var chunk struct {
			Response string `json:"response"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		fullResponse.WriteString(chunk.Response)

		// Broadcast streaming token
		h.WSHub.BroadcastToRoom(roomID, ws.Message{
			Type: "chat:ai_stream",
			Data: map[string]interface{}{
				"room_id": roomID,
				"token":   chunk.Response,
				"partial": fullResponse.String(),
			},
		})
	}

	// Save the complete AI response
	responseText := strings.TrimSpace(fullResponse.String())
	if responseText == "" {
		responseText = "I'm sorry, I wasn't able to generate a response."
	}

	aiMsg2 := models.ChatMessage{
		RoomID:     roomID,
		SenderName: "Jarvis",
		Role:       "assistant",
		Status:     "sent",
		Content:    responseText,
		Type:       "text",
	}
	h.DB.Create(&aiMsg2)

	// Update room preview
	preview := responseText
	if len(preview) > 100 {
		preview = preview[:100] + "..."
	}
	now := time.Now()
	h.DB.Model(&models.ChatRoom{}).Where("id = ?", roomID).Updates(map[string]interface{}{
		"last_msg_text": preview,
		"last_msg_at":   now,
		"last_msg_by":   "Jarvis",
	})

	// Broadcast the final saved message
	h.WSHub.BroadcastToRoom(roomID, ws.Message{
		Type: "chat:message",
		Data: aiMsg2,
	})

	// Broadcast idle status
	h.WSHub.BroadcastToRoom(roomID, ws.Message{
		Type: "chat:ai_status",
		Data: map[string]interface{}{
			"room_id": roomID,
			"status":  "idle",
		},
	})
}

// saveAIError saves an error message from the AI and broadcasts it.
// readImageAsBase64 reads a local upload path and returns a data URI for the vision model.
func readImageAsBase64(mediaURL string) string {
	// mediaURL is like "/uploads/chat/abc.jpg" — resolve to local path
	// Try both relative and common paths for robustness
	possiblePaths := []string{
		"." + mediaURL, // "./uploads/chat/abc.jpg"
		mediaURL,       // absolute or configured path
	}

	var data []byte
	var err error
	for _, path := range possiblePaths {
		data, err = os.ReadFile(path)
		if err == nil {
			break
		}
	}

	if err != nil {
		log.Printf("[chat-ai] failed to read image %s: %v", mediaURL, err)
		return ""
	}
	ext := strings.ToLower(filepath.Ext(mediaURL))
	mime := "image/jpeg"
	switch ext {
	case ".png":
		mime = "image/png"
	case ".gif":
		mime = "image/gif"
	case ".webp":
		mime = "image/webp"
	}
	return fmt.Sprintf("data:%s;base64,%s", mime, base64.StdEncoding.EncodeToString(data))
}

func (h *ChatHandler) saveAIError(roomID uint, errorMsg string) {
	msg := models.ChatMessage{
		RoomID:     roomID,
		SenderName: "Jarvis",
		Role:       "assistant",
		Status:     "sent",
		Content:    errorMsg,
		Type:       "text",
	}
	h.DB.Create(&msg)

	h.WSHub.BroadcastToRoom(roomID, ws.Message{
		Type: "chat:message",
		Data: msg,
	})

	h.WSHub.BroadcastToRoom(roomID, ws.Message{
		Type: "chat:ai_status",
		Data: map[string]interface{}{
			"room_id": roomID,
			"status":  "idle",
		},
	})
}
