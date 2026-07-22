package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"jarvishomeassist-brain/internal/config"
	"jarvishomeassist-brain/internal/handlers"
	"jarvishomeassist-brain/internal/models"
	"jarvishomeassist-brain/internal/testutil"
)

// newResumeRouter wires the resume handler behind a stub auth middleware that
// reads the acting user from the X-Test-User header, mirroring what JWTAuth
// provides via c.Set("user_id").
func newResumeRouter(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := testutil.NewTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.ResumeProfile{}, &models.GeneratedResume{}, &models.Setting{}))
	h := &handlers.ResumeHandler{DB: db, Cfg: &config.Config{}}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		uid, _ := strconv.Atoi(c.GetHeader("X-Test-User"))
		c.Set("user_id", uint(uid))
		c.Next()
	})
	r.GET("/resume/profile", h.GetProfile)
	r.PUT("/resume/profile", h.UpdateProfile)
	r.GET("/resume/generated", h.ListGenerated)
	r.GET("/resume/generated/:id", h.GetGenerated)
	r.DELETE("/resume/generated/:id", h.DeleteGenerated)
	return r, db
}

func doResume(r *gin.Engine, method, path, user, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	var req *http.Request
	if body != "" {
		req, _ = http.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req, _ = http.NewRequest(method, path, nil)
	}
	req.Header.Set("X-Test-User", user)
	r.ServeHTTP(w, req)
	return w
}

func TestResumeProfile_RoundTripAndOwnerScoping(t *testing.T) {
	r, _ := newResumeRouter(t)

	// User 1 saves a profile.
	w := doResume(r, http.MethodPut, "/resume/profile", "1",
		`{"contact":{"name":"A"},"summary":"dev"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	// User 1 reads it back.
	w = doResume(r, http.MethodGet, "/resume/profile", "1", "")
	require.Equal(t, http.StatusOK, w.Code)
	var got struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Equal(t, "dev", got.Data["summary"])

	// User 2 sees an empty profile — never user 1's data.
	w = doResume(r, http.MethodGet, "/resume/profile", "2", "")
	require.Equal(t, http.StatusOK, w.Code)
	var other struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &other))
	require.Empty(t, other.Data)

	// Updating replaces, not duplicates (one row per user).
	w = doResume(r, http.MethodPut, "/resume/profile", "1", `{"summary":"updated"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	w = doResume(r, http.MethodGet, "/resume/profile", "1", "")
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Equal(t, "updated", got.Data["summary"])

	// Invalid JSON is rejected.
	w = doResume(r, http.MethodPut, "/resume/profile", "1", `{"broken`)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestGeneratedResume_OwnerScoping(t *testing.T) {
	r, db := newResumeRouter(t)

	own := models.GeneratedResume{UserID: 1, JobTitle: "Eng", Content: datatypes.JSON(`{"summary":"x"}`)}
	theirs := models.GeneratedResume{UserID: 2, JobTitle: "Mgr", Content: datatypes.JSON(`{"summary":"y"}`)}
	require.NoError(t, db.Create(&own).Error)
	require.NoError(t, db.Create(&theirs).Error)

	// List returns only the caller's rows.
	w := doResume(r, http.MethodGet, "/resume/generated", "1", "")
	require.Equal(t, http.StatusOK, w.Code)
	var list struct {
		Resumes []models.GeneratedResume `json:"resumes"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
	require.Len(t, list.Resumes, 1)
	require.Equal(t, "Eng", list.Resumes[0].JobTitle)

	// Reading another user's resume is a 404, not a leak.
	w = doResume(r, http.MethodGet, "/resume/generated/"+strconv.Itoa(int(theirs.ID)), "1", "")
	require.Equal(t, http.StatusNotFound, w.Code)

	// Deleting another user's resume is a 404 and leaves the row intact.
	w = doResume(r, http.MethodDelete, "/resume/generated/"+strconv.Itoa(int(theirs.ID)), "1", "")
	require.Equal(t, http.StatusNotFound, w.Code)
	var count int64
	db.Model(&models.GeneratedResume{}).Count(&count)
	require.EqualValues(t, 2, count)

	// Owner can delete their own.
	w = doResume(r, http.MethodDelete, "/resume/generated/"+strconv.Itoa(int(own.ID)), "1", "")
	require.Equal(t, http.StatusOK, w.Code)
}
