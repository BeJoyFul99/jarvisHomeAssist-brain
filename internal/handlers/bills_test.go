package handlers_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

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
