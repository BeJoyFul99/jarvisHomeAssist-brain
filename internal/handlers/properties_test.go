package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"jarvishomeassist-brain/internal/handlers"
	"jarvishomeassist-brain/internal/models"
	"jarvishomeassist-brain/internal/testutil"
)

func newPropertyRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := testutil.NewTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.Property{}))
	h := &handlers.PropertyHandler{DB: db}
	r := gin.New()
	r.GET("/properties", h.List)
	r.POST("/properties", h.Create)
	r.PATCH("/properties/:id", h.Update)
	r.DELETE("/properties/:id", h.Delete)
	return r
}

func TestPropertyHandler_CreateListUpdateDelete(t *testing.T) {
	r := newPropertyRouter(t)

	// Create
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/properties",
		bytes.NewBufferString(`{"name":"Home","address":"456 Example Blvd","provider":"powerstream","rate_class":"Residential"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var created models.Property
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	require.NotZero(t, created.ID)

	// List
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/properties", nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	var list []models.Property
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
	require.Len(t, list, 1)

	// Update
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodPatch, "/properties/1",
		bytes.NewBufferString(`{"name":"Cottage"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	// Soft-delete
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodDelete, "/properties/1", nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusNoContent, w.Code)

	// List excludes soft-deleted
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/properties", nil)
	r.ServeHTTP(w, req)
	var afterDel []models.Property
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &afterDel))
	require.Len(t, afterDel, 0)
}
