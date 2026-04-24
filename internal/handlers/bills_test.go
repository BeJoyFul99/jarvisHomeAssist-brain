package handlers_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"jarvishomeassist-brain/internal/handlers"
	"jarvishomeassist-brain/internal/logger"
	"jarvishomeassist-brain/internal/models"
	"jarvishomeassist-brain/internal/sse"
	"jarvishomeassist-brain/internal/testutil"
	"jarvishomeassist-brain/internal/workers"
)

type fakeStore struct{ put [][]byte }

func (f *fakeStore) Put(_ uint, _ string, hash string, data []byte) (string, error) {
	f.put = append(f.put, data)
	return "bills/" + hash + ".pdf", nil
}
func (f *fakeStore) Get(string) ([]byte, error) { return nil, nil }
func (f *fakeStore) Delete(string) error         { return nil }

func newBillRouter(t *testing.T) (*gin.Engine, *gorm.DB, chan workers.ExtractionJob, *fakeStore, *handlers.BillHandler) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := testutil.NewTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.Property{}, &models.UtilityBill{}))
	prop := models.Property{Name: "Home", Address: "1", Provider: "powerstream", IsActive: true}
	require.NoError(t, db.Create(&prop).Error)

	log, _ := logger.New(t.TempDir())
	t.Cleanup(func() { log.Close() })
	hub := sse.NewHub(log)
	jobs := make(chan workers.ExtractionJob, 4)
	store := &fakeStore{}

	h := &handlers.BillHandler{DB: db, Store: store, Jobs: jobs, Hub: hub, Log: log}
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("user_id", uint(1)); c.Next() })
	r.POST("/utility-bills/upload", h.Upload)
	r.POST("/utility-bills", h.ManualCreate)
	r.POST("/utility-bills/:id/reextract", h.Reextract)

	return r, db, jobs, store, h
}

func mkMultipart(t *testing.T, propertyID string, filename string, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	_ = mw.WriteField("property_id", propertyID)
	fw, _ := mw.CreateFormFile("file", filename)
	_, _ = fw.Write(data)
	_ = mw.Close()
	return body, mw.FormDataContentType()
}

func seedBillWithChildren(t *testing.T, db *gorm.DB, propertyID uint) models.UtilityBill {
	t.Helper()
	require.NoError(t, db.AutoMigrate(&models.UtilityBillLineItem{}, &models.UtilityBillMeter{}))
	method := "structured"
	conf := 95
	bill := models.UtilityBill{
		PropertyID: propertyID, UploadedBy: 1,
		FileHash: "h" + strconv.Itoa(int(propertyID)) + "x" + strconv.Itoa(int(time.Now().UnixNano())),
		FilePath: "bills/x.pdf", Currency: "CAD", PaymentStatus: "unpaid",
		IngestionSource: "manual_upload", ExtractionStatus: "completed",
		ExtractionMethod: &method, ExtractionConfidence: &conf,
		TotalAmount:   123.45,
		StatementDate: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		DueDate:       time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
	}
	require.NoError(t, db.Create(&bill).Error)
	require.NoError(t, db.Create(&models.UtilityBillLineItem{
		BillID: bill.ID, UtilityType: "electricity", Category: "energy",
		Description: "Tier 1", Amount: 20.00,
	}).Error)
	require.NoError(t, db.Create(&models.UtilityBillMeter{
		BillID: bill.ID, MeterType: "electric", MeterNumber: "E1",
		Usage: 200, Multiplier: 40,
	}).Error)
	return bill
}

func TestBillHandler_Upload_Returns202AndEnqueues(t *testing.T) {
	r, db, jobs, store, _ := newBillRouter(t)
	pdf := append([]byte("%PDF-1.4\n"), bytes.Repeat([]byte{0x00}, 256)...)
	body, ct := mkMultipart(t, "1", "bill.pdf", pdf)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/utility-bills/upload", body)
	req.Header.Set("Content-Type", ct)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	var resp struct {
		BillID uint `json:"bill_id"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotZero(t, resp.BillID)

	select {
	case job := <-jobs:
		require.Equal(t, resp.BillID, job.BillID)
	default:
		t.Fatal("no job enqueued")
	}

	var bill models.UtilityBill
	require.NoError(t, db.First(&bill, resp.BillID).Error)
	require.Equal(t, "processing", bill.ExtractionStatus)
	require.Equal(t, "manual_upload", bill.IngestionSource)
	require.NotEmpty(t, store.put)

	sum := sha256.Sum256(pdf)
	require.Equal(t, hex.EncodeToString(sum[:]), bill.FileHash)
}

func TestBillHandler_Upload_Duplicate_Returns409(t *testing.T) {
	r, db, _, _, _ := newBillRouter(t)
	pdf := append([]byte("%PDF-1.4\n"), bytes.Repeat([]byte{0x00}, 256)...)

	do := func() *httptest.ResponseRecorder {
		body, ct := mkMultipart(t, "1", "bill.pdf", pdf)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/utility-bills/upload", body)
		req.Header.Set("Content-Type", ct)
		r.ServeHTTP(w, req)
		return w
	}
	require.Equal(t, http.StatusAccepted, do().Code)
	require.Equal(t, http.StatusConflict, do().Code)

	var count int64
	db.Model(&models.UtilityBill{}).Count(&count)
	require.Equal(t, int64(1), count)
}

func TestBillHandler_Upload_RejectsNonPDF(t *testing.T) {
	r, _, _, _, _ := newBillRouter(t)
	body, ct := mkMultipart(t, "1", "bill.png", []byte("\x89PNG\r\n\x1a\n"))
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/utility-bills/upload", body)
	req.Header.Set("Content-Type", ct)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestBillHandler_ManualCreate(t *testing.T) {
	r, db, _, _, _ := newBillRouter(t)

	body := `{"property_id":1,"statement_date":"2026-04-01","due_date":"2026-04-21","bill_type":"REGULAR","total_amount":100,"currency":"CAD"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/utility-bills", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var bill models.UtilityBill
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &bill))
	require.Equal(t, "manual_entry", bill.IngestionSource)
	require.Equal(t, "completed", bill.ExtractionStatus)
	require.NotNil(t, bill.ExtractionMethod)
	require.Equal(t, "manual", *bill.ExtractionMethod)

	var count int64
	db.Model(&models.UtilityBill{}).Count(&count)
	require.Equal(t, int64(1), count)
}

func TestBillHandler_Reextract_ResubmitsJob(t *testing.T) {
	r, db, jobs, _, _ := newBillRouter(t)

	bill := models.UtilityBill{
		PropertyID: 1, UploadedBy: 1, FileHash: "abc", FilePath: "bills/x.pdf",
		Currency: "CAD", PaymentStatus: "unpaid", IngestionSource: "manual_upload",
		ExtractionStatus: "failed",
	}
	require.NoError(t, db.Create(&bill).Error)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/utility-bills/"+strconv.Itoa(int(bill.ID))+"/reextract", nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusAccepted, w.Code)

	select {
	case job := <-jobs:
		require.Equal(t, bill.ID, job.BillID)
	default:
		t.Fatal("no job enqueued")
	}

	var reloaded models.UtilityBill
	require.NoError(t, db.First(&reloaded, bill.ID).Error)
	require.Equal(t, "processing", reloaded.ExtractionStatus)
}

func TestBillHandler_Reextract_404(t *testing.T) {
	r, _, _, _, _ := newBillRouter(t)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/utility-bills/999/reextract", nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestBillHandler_Reextract_ManualEntry_400(t *testing.T) {
	r, db, _, _, _ := newBillRouter(t)

	method := "manual"
	bill := models.UtilityBill{
		PropertyID: 1, UploadedBy: 1, FileHash: "m1",
		Currency: "CAD", PaymentStatus: "unpaid",
		IngestionSource: "manual_entry", ExtractionStatus: "completed",
		ExtractionMethod: &method,
	}
	require.NoError(t, db.Create(&bill).Error)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost,
		"/utility-bills/"+strconv.Itoa(int(bill.ID))+"/reextract", nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestBillHandler_List_FiltersByProperty(t *testing.T) {
	r, db, _, _, _ := newBillRouter(t)
	r.GET("/utility-bills", (&handlers.BillHandler{DB: db}).List)

	prop2 := models.Property{Name: "Cottage", Address: "2", Provider: "powerstream", IsActive: true}
	require.NoError(t, db.Create(&prop2).Error)

	seedBillWithChildren(t, db, 1)
	seedBillWithChildren(t, db, prop2.ID)
	seedBillWithChildren(t, db, 1)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/utility-bills?property_id=1", nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	var list []models.UtilityBill
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
	require.Len(t, list, 2)
}

func TestBillHandler_List_FiltersByStatus(t *testing.T) {
	r, db, _, _, _ := newBillRouter(t)
	r.GET("/utility-bills", (&handlers.BillHandler{DB: db}).List)

	_ = seedBillWithChildren(t, db, 1)
	b2 := seedBillWithChildren(t, db, 1)
	b2.ExtractionStatus = "needs_review"
	require.NoError(t, db.Save(&b2).Error)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/utility-bills?status=needs_review", nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	var list []models.UtilityBill
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
	require.Len(t, list, 1)
	require.Equal(t, "needs_review", list[0].ExtractionStatus)
}

func TestBillHandler_Get_ReturnsLineItemsAndMeters(t *testing.T) {
	r, db, _, _, _ := newBillRouter(t)
	h := &handlers.BillHandler{DB: db}
	r.GET("/utility-bills/:id", h.Get)

	bill := seedBillWithChildren(t, db, 1)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/utility-bills/"+strconv.Itoa(int(bill.ID)), nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Bill      models.UtilityBill           `json:"bill"`
		LineItems []models.UtilityBillLineItem `json:"line_items"`
		Meters    []models.UtilityBillMeter    `json:"meters"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, bill.ID, resp.Bill.ID)
	require.Len(t, resp.LineItems, 1)
	require.Len(t, resp.Meters, 1)
}

func TestBillHandler_Get_404(t *testing.T) {
	r, db, _, _, _ := newBillRouter(t)
	r.GET("/utility-bills/:id", (&handlers.BillHandler{DB: db}).Get)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/utility-bills/999", nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusNotFound, w.Code)
}
