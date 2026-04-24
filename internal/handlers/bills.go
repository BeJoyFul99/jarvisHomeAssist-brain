package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"jarvishomeassist-brain/internal/bills"
	"jarvishomeassist-brain/internal/logger"
	"jarvishomeassist-brain/internal/models"
	"jarvishomeassist-brain/internal/sse"
	"jarvishomeassist-brain/internal/workers"
)

// BillHandler manages utility bill CRUD and uploads.
type BillHandler struct {
	DB    *gorm.DB
	Store bills.BillStore
	Jobs  chan<- workers.ExtractionJob
	Hub   *sse.Hub
	Log   *logger.Logger
}

// POST /api/v1/utility-bills/upload — multipart `property_id`, `file`.
func (h *BillHandler) Upload(c *gin.Context) {
	userID, _ := c.Get("user_id")
	uid, _ := userID.(uint)
	if uid == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "auth required"})
		return
	}

	propertyID, err := strconv.ParseUint(c.PostForm("property_id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid property_id"})
		return
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file is required"})
		return
	}
	if fileHeader.Size > bills.MaxPDFBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "pdf exceeds 10MB limit"})
		return
	}

	f, err := fileHeader.Open()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "open upload"})
		return
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "read upload"})
		return
	}

	if err := bills.Validate(data); err != nil {
		switch {
		case errors.Is(err, bills.ErrTooLarge):
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": err.Error()})
		case errors.Is(err, bills.ErrEncrypted):
			c.JSON(http.StatusBadRequest, gin.H{"error": "pdf is encrypted — please decrypt before upload"})
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		}
		return
	}

	sum := sha256.Sum256(data)
	fileHash := hex.EncodeToString(sum[:])

	var existing models.UtilityBill
	if err := h.DB.Where("property_id = ? AND file_hash = ?", propertyID, fileHash).First(&existing).Error; err == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "duplicate bill for this property", "bill_id": existing.ID})
		return
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "dedup check"})
		return
	}

	bill := models.UtilityBill{
		PropertyID:       uint(propertyID),
		UploadedBy:       uid,
		FileHash:         fileHash,
		Currency:         "CAD",
		PaymentStatus:    "unpaid",
		IngestionSource:  "manual_upload",
		ExtractionStatus: "processing",
	}
	if err := h.DB.Create(&bill).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create bill"})
		return
	}

	period := bill.CreatedAt.Format("2006-01")
	path, err := h.Store.Put(uint(propertyID), period, fileHash, data)
	if err != nil {
		h.DB.Delete(&bill)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "store bill pdf"})
		return
	}
	bill.FilePath = path
	if err := h.DB.Save(&bill).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update file_path"})
		return
	}

	select {
	case h.Jobs <- workers.ExtractionJob{BillID: bill.ID}:
	default:
		h.Log.Warn("bills", fmt.Sprintf("extraction queue full, dropping bill %d", bill.ID))
		bill.ExtractionStatus = "failed"
		errStr := "extraction queue saturated"
		bill.ExtractionError = &errStr
		h.DB.Save(&bill)
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "extraction queue saturated, try again"})
		return
	}

	c.JSON(http.StatusAccepted, gin.H{"bill_id": bill.ID})
}
