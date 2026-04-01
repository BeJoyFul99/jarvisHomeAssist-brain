package handlers

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"jarvishomeassist-brain/internal/config"
	"jarvishomeassist-brain/internal/logger"
	"jarvishomeassist-brain/internal/models"
	"jarvishomeassist-brain/internal/ws"
)

// NotificationHandler manages notification CRUD and push delivery.
type NotificationHandler struct {
	DB    *gorm.DB
	WSHub *ws.Hub
	Cfg   *config.Config
	Log   *logger.Logger
}

// ── helpers ──────────────────────────────────────────────────

func (h *NotificationHandler) currentUser(c *gin.Context) (*models.User, bool) {
	email, _ := c.Get("user_email")
	var user models.User
	if err := h.DB.Where("email = ?", email).Take(&user).Error; err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user not found"})
		return nil, false
	}
	return &user, true
}

// ── List ─────────────────────────────────────────────────────

func (h *NotificationHandler) List(c *gin.Context) {
	user, ok := h.currentUser(c)
	if !ok {
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	perPage, _ := strconv.Atoi(c.DefaultQuery("per_page", "50"))
	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > 100 {
		perPage = 50
	}

	q := h.DB.Where("user_id = ?", user.ID)

	// Optional filters
	if cat := c.Query("category"); cat != "" {
		q = q.Where("category = ?", cat)
	}
	if readFilter := c.Query("read"); readFilter != "" {
		q = q.Where("read = ?", readFilter == "true")
	}

	var total int64
	q.Model(&models.Notification{}).Count(&total)

	var notifs []models.Notification
	q.Order("created_at DESC").
		Offset((page - 1) * perPage).
		Limit(perPage).
		Find(&notifs)

	h.Log.Info("notification", fmt.Sprintf("user %s listed notifications (page %d, total %d)", user.Email, page, total))
	c.JSON(http.StatusOK, gin.H{
		"notifications": notifs,
		"total":         total,
		"page":          page,
		"per_page":      perPage,
	})
}

// ── UnreadCount ──────────────────────────────────────────────

func (h *NotificationHandler) UnreadCount(c *gin.Context) {
	user, ok := h.currentUser(c)
	if !ok {
		return
	}

	var count int64
	h.DB.Model(&models.Notification{}).Where("user_id = ? AND read = false", user.ID).Count(&count)
	c.JSON(http.StatusOK, gin.H{"count": count})
}

// ── Create ───────────────────────────────────────────────────

type createNotifReq struct {
	Title       string  `json:"title" binding:"required"`
	Message     string  `json:"message" binding:"required"`
	Type        string  `json:"type"`
	Category    string  `json:"category"`
	ActionURL   *string `json:"action_url"`
	ScheduledAt *string `json:"scheduled_at"` // ISO 8601
	ExpiresAt   *string `json:"expires_at"`
	UserID      *uint   `json:"user_id"` // admin can target another user
}

func (h *NotificationHandler) Create(c *gin.Context) {
	user, ok := h.currentUser(c)
	if !ok {
		return
	}

	var req createNotifReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	targetUserID := user.ID
	// Only admins can create notifications for other users
	if req.UserID != nil && *req.UserID != user.ID {
		role, _ := c.Get("user_role")
		if role != "administrator" {
			c.JSON(http.StatusForbidden, gin.H{"error": "only admins can send notifications to other users"})
			return
		}
		targetUserID = *req.UserID
	}

	notif := models.Notification{
		UserID:    targetUserID,
		Title:     req.Title,
		Message:   req.Message,
		Type:      defaultStr(req.Type, "info"),
		Category:  defaultStr(req.Category, "reminder"),
		ActionURL: req.ActionURL,
	}

	if req.ScheduledAt != nil {
		t, err := time.Parse(time.RFC3339, *req.ScheduledAt)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid scheduled_at format, use RFC3339"})
			return
		}
		notif.ScheduledAt = &t
		notif.Fired = false
	}

	if req.ExpiresAt != nil {
		t, err := time.Parse(time.RFC3339, *req.ExpiresAt)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid expires_at format, use RFC3339"})
			return
		}
		notif.ExpiresAt = &t
	}

	if err := h.DB.Create(&notif).Error; err != nil {
		h.Log.Error("notification", fmt.Sprintf("failed to create notification for user %d: %v", targetUserID, err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create notification"})
		return
	}

	if notif.ScheduledAt != nil {
		h.Log.Info("notification", fmt.Sprintf("scheduled reminder #%d for user %d at %s: %s", notif.ID, targetUserID, notif.ScheduledAt.Format(time.RFC3339), notif.Title))
	} else {
		h.Log.Info("notification", fmt.Sprintf("created notification #%d for user %d: %s", notif.ID, targetUserID, notif.Title))
		h.deliver(notif)
	}

	c.JSON(http.StatusCreated, notif)
}

// ── MarkRead ─────────────────────────────────────────────────

func (h *NotificationHandler) MarkRead(c *gin.Context) {
	user, ok := h.currentUser(c)
	if !ok {
		return
	}

	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	result := h.DB.Model(&models.Notification{}).
		Where("id = ? AND user_id = ?", id, user.ID).
		Update("read", true)

	if result.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "notification not found"})
		return
	}
	h.Log.Info("notification", fmt.Sprintf("user %s marked notification #%d as read", user.Email, id))
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// ── MarkAllRead ──────────────────────────────────────────────

func (h *NotificationHandler) MarkAllRead(c *gin.Context) {
	user, ok := h.currentUser(c)
	if !ok {
		return
	}

	result := h.DB.Model(&models.Notification{}).
		Where("user_id = ? AND read = false", user.ID).
		Update("read", true)

	h.Log.Info("notification", fmt.Sprintf("user %s marked all notifications as read (%d updated)", user.Email, result.RowsAffected))
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// ── Delete ───────────────────────────────────────────────────

func (h *NotificationHandler) Delete(c *gin.Context) {
	user, ok := h.currentUser(c)
	if !ok {
		return
	}

	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	result := h.DB.Where("id = ? AND user_id = ?", id, user.ID).Delete(&models.Notification{})
	if result.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "notification not found"})
		return
	}
	h.Log.Info("notification", fmt.Sprintf("user %s deleted notification #%d", user.Email, id))
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// ── ClearAll ─────────────────────────────────────────────────

func (h *NotificationHandler) ClearAll(c *gin.Context) {
	user, ok := h.currentUser(c)
	if !ok {
		return
	}

	result := h.DB.Where("user_id = ?", user.ID).Delete(&models.Notification{})
	h.Log.Info("notification", fmt.Sprintf("user %s cleared all notifications (%d deleted)", user.Email, result.RowsAffected))
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// ── Push subscription endpoints ──────────────────────────────

func (h *NotificationHandler) VAPIDKey(c *gin.Context) {
	if h.Cfg.VAPIDPublicKey == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "push not configured"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"public_key": h.Cfg.VAPIDPublicKey})
}

func (h *NotificationHandler) Subscribe(c *gin.Context) {
	user, ok := h.currentUser(c)
	if !ok {
		return
	}

	var req struct {
		Endpoint  string `json:"endpoint" binding:"required"`
		KeyP256dh string `json:"key_p256dh" binding:"required"`
		KeyAuth   string `json:"key_auth" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	sub := models.PushSubscription{
		UserID:    user.ID,
		Endpoint:  req.Endpoint,
		KeyP256dh: req.KeyP256dh,
		KeyAuth:   req.KeyAuth,
		UserAgent: c.GetHeader("User-Agent"),
	}

	// Upsert by endpoint
	result := h.DB.Where("endpoint = ?", req.Endpoint).Assign(sub).FirstOrCreate(&sub)
	if result.Error != nil {
		h.Log.Error("push", fmt.Sprintf("failed to save push subscription for user %s: %v", user.Email, result.Error))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save subscription"})
		return
	}

	h.Log.Info("push", fmt.Sprintf("user %s subscribed to push notifications (endpoint: %s...)", user.Email, truncate(req.Endpoint, 60)))
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *NotificationHandler) Unsubscribe(c *gin.Context) {
	user, ok := h.currentUser(c)
	if !ok {
		return
	}

	var req struct {
		Endpoint string `json:"endpoint" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.DB.Where("user_id = ? AND endpoint = ?", user.ID, req.Endpoint).Delete(&models.PushSubscription{})
	h.Log.Info("push", fmt.Sprintf("user %s unsubscribed from push notifications", user.Email))
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// ── deliver sends a notification via WebSocket and Web Push ──

func (h *NotificationHandler) deliver(notif models.Notification) {
	h.Log.Info("notification", fmt.Sprintf("delivering notification #%d to user %d via WebSocket + push: %s", notif.ID, notif.UserID, notif.Title))

	// Real-time WebSocket delivery
	h.WSHub.SendToUser(notif.UserID, ws.Message{
		Type: "notification:new",
		Data: notif,
	})

	// Web Push — include category and action_url so the service worker
	// can apply chat-specific behaviour (grouping, navigation, etc.)
	actionURL := ""
	if notif.ActionURL != nil {
		actionURL = *notif.ActionURL
	}
	h.sendPush(notif.UserID, notif.Title, notif.Message, notif.Category, actionURL)
}

// SendPush sends a Web Push notification to all devices registered for a user.
// Public wrapper kept for backward compat (reminder worker, etc.).
func (h *NotificationHandler) SendPush(userID uint, title, message string) {
	h.sendPush(userID, title, message, "", "")
}

func (h *NotificationHandler) sendPush(userID uint, title, message, category, actionURL string) {
	if h.Cfg.VAPIDPublicKey == "" || h.Cfg.VAPIDPrivateKey == "" {
		return // push not configured
	}

	var subs []models.PushSubscription
	h.DB.Where("user_id = ?", userID).Find(&subs)

	payload := fmt.Sprintf(`{"title":%q,"message":%q,"category":%q,"action_url":%q}`, title, message, category, actionURL)

	for _, sub := range subs {
		s := &webpush.Subscription{
			Endpoint: sub.Endpoint,
			Keys: webpush.Keys{
				P256dh: sub.KeyP256dh,
				Auth:   sub.KeyAuth,
			},
		}

		resp, err := webpush.SendNotification([]byte(payload), s, &webpush.Options{
			Subscriber:      h.Cfg.VAPIDContact,
			VAPIDPublicKey:  h.Cfg.VAPIDPublicKey,
			VAPIDPrivateKey: h.Cfg.VAPIDPrivateKey,
			TTL:             60 * 60, // 1 hour
		})
		if err != nil {
			h.Log.Error("push", fmt.Sprintf("send error for endpoint %s: %v", sub.Endpoint, err))
			continue
		}
		resp.Body.Close()

		// Remove expired/invalid subscriptions
		if resp.StatusCode == 404 || resp.StatusCode == 410 {
			h.DB.Delete(&sub)
			h.Log.Info("push", fmt.Sprintf("removed expired subscription %d", sub.ID))
		}
	}
}

// DeliverChatNotification creates a persisted notification for a new chat
// message and delivers it via WebSocket + Web Push.
func (h *NotificationHandler) DeliverChatNotification(userID uint, title, body, actionURL string) {
	notif := models.Notification{
		UserID:    userID,
		Title:     title,
		Message:   body,
		Type:      "info",
		Category:  "chat",
		ActionURL: &actionURL,
	}
	if err := h.DB.Create(&notif).Error; err != nil {
		h.Log.Error("notification", fmt.Sprintf("failed to create chat notification: %v", err))
		return
	}
	h.deliver(notif)
}

func defaultStr(val, fallback string) string {
	if val == "" {
		return fallback
	}
	return val
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
