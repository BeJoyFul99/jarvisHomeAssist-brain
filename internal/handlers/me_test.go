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
	"gorm.io/gorm"

	"jarvishomeassist-brain/internal/handlers"
	"jarvishomeassist-brain/internal/models"
	"jarvishomeassist-brain/internal/testutil"
)

// newMeRouter wires the me handler behind a stub auth middleware that reads
// the acting user from the X-Test-User header, mirroring what JWTAuth
// provides via c.Set("user_id").
func newMeRouter(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := testutil.NewTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.User{}))
	h := &handlers.MeHandler{DB: db}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		uid, _ := strconv.Atoi(c.GetHeader("X-Test-User"))
		c.Set("user_id", uint(uid))
		c.Next()
	})
	r.GET("/me", h.Get)
	r.PATCH("/me", h.Update)
	return r, db
}

func doMe(r *gin.Engine, method, path, user, body string) *httptest.ResponseRecorder {
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

func mkMeUser(t *testing.T, db *gorm.DB, email, name string, role models.Role) models.User {
	t.Helper()
	u := models.User{Email: email, DisplayName: name, Role: role}
	require.NoError(t, u.SetPassword("test-password"))
	require.NoError(t, db.Create(&u).Error)
	return u
}

func TestMe_GetReturnsOwnProfile(t *testing.T) {
	r, db := newMeRouter(t)
	u := mkMeUser(t, db, "alice@test.local", "Alice", models.RoleFamilyMember)

	w := doMe(r, http.MethodGet, "/me", strconv.Itoa(int(u.ID)), "")
	require.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		User struct {
			Email       string `json:"email"`
			DisplayName string `json:"display_name"`
			Role        string `json:"role"`
		} `json:"user"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, "alice@test.local", resp.User.Email)
	require.Equal(t, "Alice", resp.User.DisplayName)
	require.Equal(t, "family_member", resp.User.Role)
}

func TestMe_UpdateDisplayNamePersists(t *testing.T) {
	r, db := newMeRouter(t)
	u := mkMeUser(t, db, "alice@test.local", "Alice", models.RoleFamilyMember)
	other := mkMeUser(t, db, "bob@test.local", "Bob", models.RoleFamilyMember)

	w := doMe(r, http.MethodPatch, "/me", strconv.Itoa(int(u.ID)),
		`{"display_name":"  Alice Cooper  "}`)
	require.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		User struct {
			DisplayName string `json:"display_name"`
		} `json:"user"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, "Alice Cooper", resp.User.DisplayName, "response carries the trimmed new name")

	var got models.User
	require.NoError(t, db.First(&got, u.ID).Error)
	require.Equal(t, "Alice Cooper", got.DisplayName, "new name is persisted")

	var untouched models.User
	require.NoError(t, db.First(&untouched, other.ID).Error)
	require.Equal(t, "Bob", untouched.DisplayName, "other users are unaffected")
}

func TestMe_UpdateRejectsEmptyName(t *testing.T) {
	r, db := newMeRouter(t)
	u := mkMeUser(t, db, "alice@test.local", "Alice", models.RoleFamilyMember)

	for _, body := range []string{`{"display_name":""}`, `{"display_name":"   "}`, `{}`} {
		w := doMe(r, http.MethodPatch, "/me", strconv.Itoa(int(u.ID)), body)
		require.Equal(t, http.StatusBadRequest, w.Code, "body %s must be rejected", body)
	}

	var got models.User
	require.NoError(t, db.First(&got, u.ID).Error)
	require.Equal(t, "Alice", got.DisplayName, "rejected updates must not change the name")
}

func TestMe_UnknownUserIs404(t *testing.T) {
	r, _ := newMeRouter(t)

	w := doMe(r, http.MethodGet, "/me", "999", "")
	require.Equal(t, http.StatusNotFound, w.Code)

	w = doMe(r, http.MethodPatch, "/me", "999", `{"display_name":"Ghost"}`)
	require.Equal(t, http.StatusNotFound, w.Code)
}
