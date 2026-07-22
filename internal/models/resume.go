package models

import (
	"time"

	"gorm.io/datatypes"
)

// ResumeProfile stores one user's master resume profile as a flexible JSON
// document (contact, summary, experiences, education, skills, certifications,
// projects). One row per user; all access is owner-scoped by UserID.
type ResumeProfile struct {
	ID        uint           `gorm:"primaryKey" json:"id"`
	UserID    uint           `gorm:"uniqueIndex;not null" json:"user_id"`
	Data      datatypes.JSON `gorm:"type:jsonb;default:'{}'" json:"data"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// GeneratedResume is a job-tailored resume produced by the AI worker from the
// user's ResumeProfile. Content holds the structured resume document returned
// by the model (summary, skills, experiences, keywords, ...).
type GeneratedResume struct {
	ID             uint           `gorm:"primaryKey" json:"id"`
	UserID         uint           `gorm:"index;not null" json:"user_id"`
	JobTitle       string         `gorm:"size:255" json:"job_title"`
	Company        string         `gorm:"size:255" json:"company"`
	JobDescription string         `gorm:"type:text" json:"job_description"`
	Model          string         `gorm:"size:255" json:"model"`
	Content        datatypes.JSON `gorm:"type:jsonb" json:"content"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}
