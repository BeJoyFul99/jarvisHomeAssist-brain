package handlers

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"jarvishomeassist-brain/internal/config"
	"jarvishomeassist-brain/internal/logger"
	"jarvishomeassist-brain/internal/models"
	"jarvishomeassist-brain/internal/sse"
	"jarvishomeassist-brain/internal/ws"
)

// AnnouncementHandler manages announcement CRUD and read tracking.
type AnnouncementHandler struct {
	DB     *gorm.DB
	Hub    *sse.Hub
	WSHub  *ws.Hub
	Cfg    *config.Config
	Log    *logger.Logger
	Notify func(userID uint, title, body, actionURL string)
}

// ── helpers ──────────────────────────────────────────────────

func (h *AnnouncementHandler) currentUser(c *gin.Context) (*models.User, bool) {
	email, _ := c.Get("user_email")
	var user models.User
	if err := h.DB.Where("email = ?", email).First(&user).Error; err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user not found"})
		return nil, false
	}
	return &user, true
}

func (h *AnnouncementHandler) totalActiveUsers() int {
	var count int64
	h.DB.Model(&models.User{}).Where("is_locked = false").Count(&count)
	return int(count)
}

func (h *AnnouncementHandler) enrichAnnouncement(a *models.Announcement) {
	var readCount int64
	h.DB.Model(&models.AnnouncementRead{}).Where("announcement_id = ?", a.ID).Count(&readCount)
	a.ReadCount = int(readCount)
	a.TotalUsers = h.totalActiveUsers()
}

// ── List (all users) ────────────────────────────────────────

func (h *AnnouncementHandler) List(c *gin.Context) {
	user, ok := h.currentUser(c)
	if !ok {
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	perPage, _ := strconv.Atoi(c.DefaultQuery("per_page", "20"))
	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > 100 {
		perPage = 20
	}

	q := h.DB.Model(&models.Announcement{})

	if cat := c.Query("category"); cat != "" {
		q = q.Where("category = ?", cat)
	}
	if priority := c.Query("priority"); priority != "" {
		q = q.Where("priority = ?", priority)
	}

	var total int64
	q.Count(&total)

	var announcements []models.Announcement
	q.Preload("Author").
		Order("pinned DESC, created_at DESC").
		Offset((page - 1) * perPage).
		Limit(perPage).
		Find(&announcements)

	totalUsers := h.totalActiveUsers()

	// Enrich with read counts + whether current user has read each
	var readAnnouncementIDs []uint
	h.DB.Model(&models.AnnouncementRead{}).
		Where("user_id = ?", user.ID).
		Pluck("announcement_id", &readAnnouncementIDs)
	readSet := make(map[uint]bool, len(readAnnouncementIDs))
	for _, id := range readAnnouncementIDs {
		readSet[id] = true
	}

	type enriched struct {
		models.Announcement
		IsRead bool `json:"is_read"`
	}

	result := make([]enriched, len(announcements))
	for i := range announcements {
		var rc int64
		h.DB.Model(&models.AnnouncementRead{}).Where("announcement_id = ?", announcements[i].ID).Count(&rc)
		announcements[i].ReadCount = int(rc)
		announcements[i].TotalUsers = totalUsers
		result[i] = enriched{
			Announcement: announcements[i],
			IsRead:       readSet[announcements[i].ID],
		}
	}

	h.Log.Info("announcement", fmt.Sprintf("user %s listed announcements (page %d, total %d)", user.Email, page, total))
	c.JSON(http.StatusOK, gin.H{
		"announcements": result,
		"total":         total,
		"page":          page,
		"per_page":      perPage,
	})
}

// ── Get single announcement ─────────────────────────────────

func (h *AnnouncementHandler) Get(c *gin.Context) {
	user, ok := h.currentUser(c)
	if !ok {
		return
	}

	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var announcement models.Announcement
	if err := h.DB.Preload("Author").First(&announcement, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "announcement not found"})
		return
	}

	h.enrichAnnouncement(&announcement)

	// Load read receipts for admin view
	role, _ := c.Get("user_role")
	if role == "administrator" {
		var reads []models.AnnouncementRead
		h.DB.Preload("User").Where("announcement_id = ?", announcement.ID).Order("read_at DESC").Find(&reads)
		announcement.Reads = reads
	}

	// Check if current user has read it
	var readRecord models.AnnouncementRead
	isRead := h.DB.Where("announcement_id = ? AND user_id = ?", announcement.ID, user.ID).First(&readRecord).Error == nil

	c.JSON(http.StatusOK, gin.H{
		"announcement": announcement,
		"is_read":      isRead,
	})
}

// ── Create (admin only) ─────────────────────────────────────

type createAnnouncementReq struct {
	Title    string `json:"title" binding:"required"`
	Body     string `json:"body" binding:"required"`
	Priority string `json:"priority"`
	Category string `json:"category"`
	Pinned   bool   `json:"pinned"`
}

func (h *AnnouncementHandler) Create(c *gin.Context) {
	user, ok := h.currentUser(c)
	if !ok {
		return
	}

	var req createAnnouncementReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	announcement := models.Announcement{
		AuthorID: user.ID,
		Title:    req.Title,
		Body:     req.Body,
		Priority: defaultStr(req.Priority, "normal"),
		Category: defaultStr(req.Category, "general"),
		Pinned:   req.Pinned,
	}

	if err := h.DB.Create(&announcement).Error; err != nil {
		h.Log.Error("announcement", fmt.Sprintf("failed to create announcement: %v", err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create announcement"})
		return
	}

	// Reload with author
	h.DB.Preload("Author").First(&announcement, announcement.ID)

	h.Log.Info("announcement", fmt.Sprintf("admin %s created announcement #%d: %s", user.Email, announcement.ID, announcement.Title))

	// Broadcast via SSE
	h.Hub.Broadcast(sse.Event{
		Type: "announcement:created",
		Data: announcement,
	})

	// Send notification to all active users (except author)
	go h.notifyAllUsers(user.ID, announcement)

	c.JSON(http.StatusCreated, announcement)
}

// ── Update (admin only) ─────────────────────────────────────

type updateAnnouncementReq struct {
	Title    *string `json:"title"`
	Body     *string `json:"body"`
	Priority *string `json:"priority"`
	Category *string `json:"category"`
	Pinned   *bool   `json:"pinned"`
}

func (h *AnnouncementHandler) Update(c *gin.Context) {
	user, ok := h.currentUser(c)
	if !ok {
		return
	}

	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var announcement models.Announcement
	if err := h.DB.First(&announcement, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "announcement not found"})
		return
	}

	var req updateAnnouncementReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	contentChanged := false
	if req.Title != nil && *req.Title != announcement.Title {
		announcement.Title = *req.Title
		contentChanged = true
	}
	if req.Body != nil && *req.Body != announcement.Body {
		announcement.Body = *req.Body
		contentChanged = true
	}
	if req.Priority != nil {
		announcement.Priority = *req.Priority
	}
	if req.Category != nil {
		announcement.Category = *req.Category
	}
	if req.Pinned != nil {
		announcement.Pinned = *req.Pinned
	}

	// Mark as edited if title or body changed
	if contentChanged {
		now := time.Now()
		announcement.EditedAt = &now
	}

	if err := h.DB.Save(&announcement).Error; err != nil {
		h.Log.Error("announcement", fmt.Sprintf("failed to update announcement #%d: %v", id, err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update announcement"})
		return
	}

	h.DB.Preload("Author").First(&announcement, announcement.ID)
	h.enrichAnnouncement(&announcement)

	h.Log.Info("announcement", fmt.Sprintf("admin %s updated announcement #%d", user.Email, id))

	h.Hub.Broadcast(sse.Event{
		Type: "announcement:updated",
		Data: announcement,
	})

	c.JSON(http.StatusOK, announcement)
}

// ── Delete (admin only) ─────────────────────────────────────

func (h *AnnouncementHandler) Delete(c *gin.Context) {
	user, ok := h.currentUser(c)
	if !ok {
		return
	}

	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)

	// Delete read receipts first
	h.DB.Where("announcement_id = ?", id).Delete(&models.AnnouncementRead{})

	result := h.DB.Delete(&models.Announcement{}, id)
	if result.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "announcement not found"})
		return
	}

	h.Log.Info("announcement", fmt.Sprintf("admin %s deleted announcement #%d", user.Email, id))

	h.Hub.Broadcast(sse.Event{
		Type: "announcement:deleted",
		Data: gin.H{"id": id},
	})

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// ── MarkRead (any user) ─────────────────────────────────────

func (h *AnnouncementHandler) MarkRead(c *gin.Context) {
	user, ok := h.currentUser(c)
	if !ok {
		return
	}

	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)

	// Verify announcement exists
	var announcement models.Announcement
	if err := h.DB.First(&announcement, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "announcement not found"})
		return
	}

	// Upsert read receipt
	read := models.AnnouncementRead{
		AnnouncementID: uint(id),
		UserID:         user.ID,
		ReadAt:         time.Now(),
	}

	result := h.DB.Where("announcement_id = ? AND user_id = ?", id, user.ID).FirstOrCreate(&read)
	if result.Error != nil {
		h.Log.Error("announcement", fmt.Sprintf("failed to mark announcement #%d as read for user %d: %v", id, user.ID, result.Error))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to mark as read"})
		return
	}

	h.Log.Info("announcement", fmt.Sprintf("user %s read announcement #%d", user.Email, id))

	// Broadcast updated read count via SSE so admin sees live updates
	h.Hub.Broadcast(sse.Event{
		Type: "announcement:read",
		Data: gin.H{
			"announcement_id": id,
			"user_id":         user.ID,
			"display_name":    user.DisplayName,
		},
	})

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// ── ReadReceipts (admin only) ───────────────────────────────

func (h *AnnouncementHandler) ReadReceipts(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)

	var reads []models.AnnouncementRead
	h.DB.Preload("User").Where("announcement_id = ?", id).Order("read_at DESC").Find(&reads)

	c.JSON(http.StatusOK, gin.H{"reads": reads})
}

// ── notifyAllUsers sends push + WS notification to all active users ──

func (h *AnnouncementHandler) notifyAllUsers(excludeUserID uint, announcement models.Announcement) {
	var users []models.User
	h.DB.Where("is_locked = false AND id != ?", excludeUserID).Find(&users)

	actionURL := "/home/announcements"
	for _, u := range users {
		if h.Notify != nil {
			h.Notify(u.ID, "New Announcement: "+announcement.Title, announcement.Body, actionURL)
		}
	}

	h.Log.Info("announcement", fmt.Sprintf("sent notifications for announcement #%d to %d users", announcement.ID, len(users)))
}
