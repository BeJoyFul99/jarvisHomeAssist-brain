package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"jarvishomeassist-brain/internal/config"
	"jarvishomeassist-brain/internal/logger"
	"jarvishomeassist-brain/internal/models"
)

// ResumeHandler serves the Resume Builder tool: per-user profile storage and
// AI-tailored resume generation. Every query is owner-scoped by the JWT user.
type ResumeHandler struct {
	DB  *gorm.DB
	Cfg *config.Config
	Log *logger.Logger
}

// maxProfileBytes caps the stored profile document (256 KB is far beyond any
// real resume profile; the cap only guards against abuse).
const maxProfileBytes = 256 * 1024

// resumeSchema is the strict output contract the model must follow.
const resumeSchema = `{
  "summary": "string — 2-4 sentence professional summary tailored to the job",
  "skills": [{"category": "string", "items": ["string"]}],
  "experiences": [{"company": "string", "role": "string", "location": "string", "start": "string", "end": "string", "bullets": ["string"]}],
  "education": [{"school": "string", "degree": "string", "field": "string", "start": "string", "end": "string", "notes": "string"}],
  "certifications": [{"name": "string", "issuer": "string", "year": "string"}],
  "projects": [{"name": "string", "description": "string", "bullets": ["string"], "technologies": ["string"]}],
  "keywords": ["string — ATS keywords from the posting that the profile genuinely supports"]
}`

const resumeSystemPrompt = `You are an expert resume writer and ATS optimization specialist. You receive a candidate's master profile (JSON) and a job posting, and produce a resume tailored to that job.

Rules:
- Use ONLY facts present in the profile. NEVER invent employers, titles, dates, degrees, certifications, skills, metrics, or technologies.
- Reorder experiences and rewrite bullet points to emphasize what matters most for this job; drop weak or irrelevant bullets and sections.
- Weave the posting's keywords in naturally where the profile genuinely supports them (ATS compatibility).
- Rewrite the summary to align with this specific position.
- Bullets are concise, start with a strong action verb, and keep any quantified results the profile provides.
- Omit array entries that are irrelevant to the job rather than padding them.
- Output STRICT JSON only — no markdown fences, no commentary — matching exactly this schema:
` + resumeSchema

func (h *ResumeHandler) userID(c *gin.Context) (uint, bool) {
	v, _ := c.Get("user_id")
	id, ok := v.(uint)
	return id, ok
}

// GetProfile returns the current user's resume profile document.
// GET /api/v1/resume/profile
func (h *ResumeHandler) GetProfile(c *gin.Context) {
	uid, ok := h.userID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	var profile models.ResumeProfile
	if err := h.DB.WithContext(ctx).Where("user_id = ?", uid).Take(&profile).Error; err != nil {
		c.JSON(http.StatusOK, gin.H{"data": gin.H{}, "updated_at": nil})
		return
	}

	var data map[string]interface{}
	if profile.Data != nil {
		_ = json.Unmarshal(profile.Data, &data)
	}
	if data == nil {
		data = map[string]interface{}{}
	}
	c.JSON(http.StatusOK, gin.H{"data": data, "updated_at": profile.UpdatedAt})
}

// UpdateProfile replaces the current user's resume profile document.
// PUT /api/v1/resume/profile
func (h *ResumeHandler) UpdateProfile(c *gin.Context) {
	uid, ok := h.userID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxProfileBytes+1))
	if err != nil || len(body) > maxProfileBytes {
		c.JSON(http.StatusBadRequest, gin.H{"error": "profile too large"})
		return
	}
	if !json.Valid(body) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	profile := models.ResumeProfile{UserID: uid, Data: datatypes.JSON(body)}
	if err := h.DB.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"data", "updated_at"}),
	}).Create(&profile).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save profile"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": json.RawMessage(body), "updated_at": profile.UpdatedAt})
}

// Generate tailors the user's profile to a job posting via the AI worker.
// POST /api/v1/resume/generate
func (h *ResumeHandler) Generate(c *gin.Context) {
	uid, ok := h.userID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	if h.Cfg.CFWorkerURL == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "ai_unavailable", "message": "AI worker is not configured"})
		return
	}

	var body struct {
		JobTitle       string `json:"job_title"`
		Company        string `json:"company"`
		JobDescription string `json:"job_description"`
		Model          string `json:"model"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	body.JobDescription = strings.TrimSpace(body.JobDescription)
	if body.JobDescription == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "job_description is required"})
		return
	}

	// Load the caller's profile — generation is meaningless without one.
	var profile models.ResumeProfile
	if err := h.DB.Where("user_id = ?", uid).Take(&profile).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "profile_empty", "message": "Fill in your profile before generating a resume"})
		return
	}
	var profileDoc map[string]interface{}
	if err := json.Unmarshal(profile.Data, &profileDoc); err != nil || len(profileDoc) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "profile_empty", "message": "Fill in your profile before generating a resume"})
		return
	}
	// Contact details are rendered client-side from the profile — no need to
	// send them to the model.
	delete(profileDoc, "contact")
	profileJSON, _ := json.Marshal(profileDoc)

	var sb strings.Builder
	sb.WriteString("CANDIDATE PROFILE (JSON):\n")
	sb.Write(profileJSON)
	sb.WriteString("\n\nJOB POSTING:\n")
	if body.JobTitle != "" {
		sb.WriteString("Title: " + body.JobTitle + "\n")
	}
	if body.Company != "" {
		sb.WriteString("Company: " + body.Company + "\n")
	}
	sb.WriteString("\n" + body.JobDescription)

	// Model: explicit request override, else the admin-selected setting;
	// empty lets the worker pick its default.
	model := strings.TrimSpace(body.Model)
	if model == "" {
		model = GetSetting(h.DB, "ai_resume_model", "")
	}

	chatReq := map[string]interface{}{
		"messages": []map[string]string{
			{"role": "system", "content": resumeSystemPrompt},
			{"role": "user", "content": sb.String()},
		},
		"tags":       []string{"resume"},
		"max_tokens": 4096,
	}
	if model != "" {
		chatReq["model"] = model
	}
	reqBody, _ := json.Marshal(chatReq)

	client := &http.Client{Timeout: 120 * time.Second}
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, h.Cfg.CFWorkerURL+"/v1/chat", bytes.NewReader(reqBody))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to build AI request"})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+h.Cfg.CFWorkerSecret)

	resp, err := client.Do(req)
	if err != nil {
		h.Log.Error("resume", fmt.Sprintf("worker request: %v", err))
		c.JSON(http.StatusBadGateway, gin.H{"error": "ai_unreachable", "message": "AI worker is unreachable"})
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if resp.StatusCode == http.StatusTooManyRequests {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "rate_limited", "message": "Daily AI limit reached — try again tomorrow"})
		return
	}
	if resp.StatusCode != http.StatusOK {
		h.Log.Error("resume", fmt.Sprintf("worker returned %d: %s", resp.StatusCode, string(respBody)))
		c.JSON(http.StatusBadGateway, gin.H{"error": "ai_error", "message": "AI worker returned an error"})
		return
	}

	var workerResp struct {
		Response string `json:"response"`
	}
	rawText := string(respBody)
	if json.Unmarshal(respBody, &workerResp) == nil && workerResp.Response != "" {
		rawText = workerResp.Response
	}

	content, err := extractResumeJSON(rawText)
	if err != nil {
		logText := rawText
		if len(logText) > 2000 {
			logText = logText[:2000] + "…"
		}
		h.Log.Error("resume", "unparseable AI output: "+logText)
		c.JSON(http.StatusBadGateway, gin.H{"error": "ai_bad_output", "message": "The model returned an unusable response — try again or pick a different model"})
		return
	}

	generated := models.GeneratedResume{
		UserID:         uid,
		JobTitle:       body.JobTitle,
		Company:        body.Company,
		JobDescription: body.JobDescription,
		Model:          model,
		Content:        content,
	}
	if err := h.DB.Create(&generated).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save resume"})
		return
	}

	c.JSON(http.StatusOK, generated)
}

// ListGenerated returns the current user's generation history (no content).
// GET /api/v1/resume/generated
func (h *ResumeHandler) ListGenerated(c *gin.Context) {
	uid, ok := h.userID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	var items []models.GeneratedResume
	if err := h.DB.WithContext(ctx).
		Select("id", "job_title", "company", "model", "created_at").
		Where("user_id = ?", uid).
		Order("created_at DESC").
		Limit(50).
		Find(&items).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list resumes"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"resumes": items})
}

// GetGenerated returns one generated resume owned by the current user.
// GET /api/v1/resume/generated/:id
func (h *ResumeHandler) GetGenerated(c *gin.Context) {
	uid, ok := h.userID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	var item models.GeneratedResume
	if err := h.DB.WithContext(ctx).Where("id = ? AND user_id = ?", c.Param("id"), uid).Take(&item).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	c.JSON(http.StatusOK, item)
}

// DeleteGenerated removes one generated resume owned by the current user.
// DELETE /api/v1/resume/generated/:id
func (h *ResumeHandler) DeleteGenerated(c *gin.Context) {
	uid, ok := h.userID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	res := h.DB.WithContext(ctx).Where("id = ? AND user_id = ?", c.Param("id"), uid).Delete(&models.GeneratedResume{})
	if res.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete"})
		return
	}
	if res.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

// extractResumeJSON pulls a JSON object out of model output that may be
// wrapped in markdown fences or surrounded by prose, and validates it.
func extractResumeJSON(raw string) (datatypes.JSON, error) {
	s := strings.TrimSpace(raw)
	if i := strings.Index(s, "```"); i != -1 {
		s = s[i+3:]
		s = strings.TrimPrefix(s, "json")
		if j := strings.Index(s, "```"); j != -1 {
			s = s[:j]
		}
	}
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start == -1 || end <= start {
		return nil, fmt.Errorf("no JSON object in output")
	}
	s = s[start : end+1]

	var doc map[string]interface{}
	if err := json.Unmarshal([]byte(s), &doc); err != nil {
		return nil, err
	}
	compact, _ := json.Marshal(doc)
	return datatypes.JSON(compact), nil
}
