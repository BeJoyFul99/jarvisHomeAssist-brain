package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

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

	// Dedup against ALL rows (including soft-deleted) because the UNIQUE
	// index ux_bill_property_hash does not honor deleted_at.
	var existing models.UtilityBill
	dupErr := h.DB.Unscoped().
		Where("property_id = ? AND file_hash = ?", propertyID, fileHash).
		First(&existing).Error

	if dupErr != nil && !errors.Is(dupErr, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "dedup check"})
		return
	}

	var bill models.UtilityBill
	if dupErr == nil && !existing.DeletedAt.Valid {
		// Live duplicate — reject.
		c.JSON(http.StatusConflict, gin.H{
			"error":   "duplicate bill for this property",
			"bill_id": existing.ID,
		})
		return
	}
	if dupErr == nil && existing.DeletedAt.Valid {
		// Re-uploading a previously-deleted bill: restore it, reset extraction,
		// and wipe children so extraction can repopulate them cleanly.
		bill = existing
		if err := h.DB.Unscoped().Model(&bill).Updates(map[string]any{
			"deleted_at":            nil,
			"uploaded_by":           uid,
			"extraction_status":     "processing",
			"extraction_method":     nil,
			"extraction_confidence": nil,
			"extraction_error":      nil,
			"raw_extracted_data":    nil,
			"ingestion_source":      "manual_upload",
		}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "restore bill"})
			return
		}
		if err := h.DB.Where("bill_id = ?", bill.ID).Delete(&models.UtilityBillLineItem{}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "wipe line items"})
			return
		}
		if err := h.DB.Where("bill_id = ?", bill.ID).Delete(&models.UtilityBillMeter{}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "wipe meters"})
			return
		}
		// Reload with the cleared deleted_at so subsequent reads see the live row.
		if err := h.DB.First(&bill, bill.ID).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "reload bill"})
			return
		}
	} else {
		// No prior row — normal create.
		bill = models.UtilityBill{
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

// POST /api/v1/utility-bills — manual entry, no PDF. Spec §3.6.
func (h *BillHandler) ManualCreate(c *gin.Context) {
	userID, _ := c.Get("user_id")
	uid, _ := userID.(uint)

	var body struct {
		PropertyID         uint    `json:"property_id" binding:"required"`
		StatementDate      string  `json:"statement_date"`
		DueDate            string  `json:"due_date"`
		BillingPeriodStart string  `json:"billing_period_start"`
		BillingPeriodEnd   string  `json:"billing_period_end"`
		BillType           string  `json:"bill_type"`
		TotalAmount        float64 `json:"total_amount"`
		PreviousBalance    float64 `json:"previous_balance"`
		LateFees           float64 `json:"late_fees"`
		Currency           string  `json:"currency"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	parseDate := func(s string) (t time.Time) { t, _ = time.Parse("2006-01-02", s); return }
	method := "manual"
	bill := models.UtilityBill{
		PropertyID:         body.PropertyID,
		UploadedBy:         uid,
		StatementDate:      parseDate(body.StatementDate),
		DueDate:            parseDate(body.DueDate),
		BillingPeriodStart: parseDate(body.BillingPeriodStart),
		BillingPeriodEnd:   parseDate(body.BillingPeriodEnd),
		BillType:           body.BillType,
		TotalAmount:        body.TotalAmount,
		PreviousBalance:    body.PreviousBalance,
		LateFees:           body.LateFees,
		Currency:           cond(body.Currency != "", body.Currency, "CAD"),
		PaymentStatus:      "unpaid",
		IngestionSource:    "manual_entry",
		ExtractionStatus:   "completed",
		ExtractionMethod:   &method,
	}
	if err := h.DB.Create(&bill).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create bill"})
		return
	}
	c.JSON(http.StatusCreated, bill)
}

// POST /api/v1/utility-bills/:id/reextract — reset and enqueue.
func (h *BillHandler) Reextract(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var bill models.UtilityBill
	if err := h.DB.First(&bill, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "bill not found"})
		return
	}
	if bill.FilePath == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bill has no PDF to reextract (manual entry)"})
		return
	}
	bill.ExtractionStatus = "processing"
	bill.ExtractionError = nil
	if err := h.DB.Save(&bill).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update status"})
		return
	}
	select {
	case h.Jobs <- workers.ExtractionJob{BillID: bill.ID}:
	default:
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "queue saturated"})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"bill_id": bill.ID})
}

// GET /api/v1/utility-bills — optional filters: property_id, status, date_from, date_to.
func (h *BillHandler) List(c *gin.Context) {
	q := h.DB.WithContext(c.Request.Context()).Model(&models.UtilityBill{}).Order("statement_date desc, id desc")
	if pid := c.Query("property_id"); pid != "" {
		q = q.Where("property_id = ?", pid)
	}
	if status := c.Query("status"); status != "" {
		q = q.Where("extraction_status = ?", status)
	}
	if from := c.Query("date_from"); from != "" {
		if t, err := time.Parse("2006-01-02", from); err == nil {
			q = q.Where("statement_date >= ?", t)
		}
	}
	if to := c.Query("date_to"); to != "" {
		if t, err := time.Parse("2006-01-02", to); err == nil {
			q = q.Where("statement_date <= ?", t)
		}
	}

	var billsOut []models.UtilityBill
	if err := q.Find(&billsOut).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list failed"})
		return
	}
	c.JSON(http.StatusOK, billsOut)
}

// GET /api/v1/utility-bills/:id — bill + line items + meters.
func (h *BillHandler) Get(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var bill models.UtilityBill
	if err := h.DB.WithContext(c.Request.Context()).First(&bill, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "bill not found"})
		return
	}
	var items []models.UtilityBillLineItem
	var meters []models.UtilityBillMeter
	h.DB.WithContext(c.Request.Context()).Where("bill_id = ?", bill.ID).Find(&items)
	h.DB.WithContext(c.Request.Context()).Where("bill_id = ?", bill.ID).Find(&meters)
	c.JSON(http.StatusOK, gin.H{"bill": bill, "line_items": items, "meters": meters})
}

// PATCH /api/v1/utility-bills/:id
func (h *BillHandler) Update(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var body map[string]any
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	allowed := map[string]bool{
		"bill_type": true, "total_amount": true, "previous_balance": true,
		"payments_received": true, "balance_forward": true, "late_fees": true,
		"currency": true, "payment_status": true, "paid_date": true, "paid_amount": true,
		"statement_date": true, "due_date": true,
		"billing_period_start": true, "billing_period_end": true,
	}
	updates := map[string]any{}
	for k, v := range body {
		if allowed[k] {
			updates[k] = v
		}
	}
	if len(updates) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no updatable fields"})
		return
	}
	var bill models.UtilityBill
	if err := h.DB.WithContext(c.Request.Context()).First(&bill, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "bill not found"})
		return
	}
	if err := h.DB.WithContext(c.Request.Context()).Model(&bill).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update"})
		return
	}
	if err := h.DB.WithContext(c.Request.Context()).First(&bill, bill.ID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "reload"})
		return
	}
	c.JSON(http.StatusOK, bill)
}

// PATCH /api/v1/utility-bills/:id/line-items/:line_id
func (h *BillHandler) UpdateLineItem(c *gin.Context) {
	billID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	lineID, err := strconv.ParseUint(c.Param("line_id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid line_id"})
		return
	}
	var body map[string]any
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	allowed := map[string]bool{
		"utility_type": true, "category": true, "description": true,
		"usage_amount": true, "usage_unit": true, "rate": true, "amount": true,
	}
	updates := map[string]any{}
	for k, v := range body {
		if allowed[k] {
			updates[k] = v
		}
	}
	var li models.UtilityBillLineItem
	if err := h.DB.WithContext(c.Request.Context()).
		Where("id = ? AND bill_id = ?", lineID, billID).
		First(&li).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "line item not found"})
		return
	}
	if err := h.DB.WithContext(c.Request.Context()).Model(&li).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update"})
		return
	}
	if err := h.DB.WithContext(c.Request.Context()).First(&li, li.ID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "reload"})
		return
	}
	c.JSON(http.StatusOK, li)
}

// POST /api/v1/utility-bills/:id/mark-paid — body: {paid_amount, paid_date (opt)}
func (h *BillHandler) MarkPaid(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var body struct {
		PaidAmount float64 `json:"paid_amount" binding:"required"`
		PaidDate   string  `json:"paid_date"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var bill models.UtilityBill
	if err := h.DB.WithContext(c.Request.Context()).First(&bill, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "bill not found"})
		return
	}
	bill.PaidAmount = &body.PaidAmount
	switch {
	case body.PaidAmount >= bill.TotalAmount:
		bill.PaymentStatus = "paid"
	case body.PaidAmount > 0:
		bill.PaymentStatus = "partial"
	default:
		bill.PaymentStatus = "unpaid"
	}
	if body.PaidDate != "" {
		if t, err := time.Parse("2006-01-02", body.PaidDate); err == nil {
			bill.PaidDate = &t
		}
	} else {
		now := time.Now().UTC()
		bill.PaidDate = &now
	}
	if err := h.DB.WithContext(c.Request.Context()).Save(&bill).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save"})
		return
	}
	c.JSON(http.StatusOK, bill)
}

// GET /api/v1/utility-bills/:id/pdf
func (h *BillHandler) DownloadPDF(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var bill models.UtilityBill
	if err := h.DB.WithContext(c.Request.Context()).First(&bill, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "bill not found"})
		return
	}
	if bill.FilePath == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "no PDF for this bill"})
		return
	}
	data, err := h.Store.Get(bill.FilePath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "read pdf"})
		return
	}

	// Meaningful filename: statement period when known, bill id otherwise.
	filename := fmt.Sprintf("utility-bill-%d.pdf", bill.ID)
	if !bill.StatementDate.IsZero() {
		filename = "utility-bill-" + bill.StatementDate.Format("2006-01") + ".pdf"
	}
	disposition := "inline"
	if c.Query("download") == "1" {
		disposition = "attachment"
	}
	c.Header("Content-Disposition", fmt.Sprintf(`%s; filename=%q`, disposition, filename))
	c.Header("Content-Length", strconv.Itoa(len(data)))
	c.Data(http.StatusOK, "application/pdf", data)
}

// DELETE /api/v1/utility-bills/:id — soft by default; ?hard=true removes row + children + PDF.
func (h *BillHandler) Delete(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	hard := c.Query("hard") == "true"

	var bill models.UtilityBill
	if err := h.DB.WithContext(c.Request.Context()).First(&bill, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "bill not found"})
		return
	}

	if !hard {
		if err := h.DB.WithContext(c.Request.Context()).Delete(&bill).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "soft delete"})
			return
		}
		c.Status(http.StatusNoContent)
		return
	}

	err = h.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("bill_id = ?", bill.ID).Delete(&models.UtilityBillLineItem{}).Error; err != nil {
			return err
		}
		if err := tx.Where("bill_id = ?", bill.ID).Delete(&models.UtilityBillMeter{}).Error; err != nil {
			return err
		}
		if err := tx.Where("bill_id = ?", bill.ID).Delete(&models.UtilityBillNotification{}).Error; err != nil {
			return err
		}
		return tx.Unscoped().Delete(&bill).Error
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "hard delete"})
		return
	}
	if bill.FilePath != "" && h.Store != nil {
		_ = h.Store.Delete(bill.FilePath)
	}
	c.Status(http.StatusNoContent)
}
