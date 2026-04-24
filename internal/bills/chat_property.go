package bills

import (
	"regexp"
	"strings"

	"gorm.io/gorm"

	"jarvishomeassist-brain/internal/models"
)

// ChatContextHints carries what the chat handler knows about the conversation.
type ChatContextHints struct {
	Message string // raw user message
	PageURL string // e.g. "/utilities/2/overview"
}

var pageURLProp = regexp.MustCompile(`/utilities/(\d+)(?:/|$)`)

// ResolveProperty runs the §5.2 fallback chain. Nil return means "ambiguous —
// caller should inject a multi-property summary and let the model disambiguate".
func ResolveProperty(db *gorm.DB, h ChatContextHints) (*models.Property, error) {
	if m := pageURLProp.FindStringSubmatch(h.PageURL); len(m) == 2 {
		var p models.Property
		if err := db.Where("id = ? AND is_active = ?", m[1], true).First(&p).Error; err == nil {
			return &p, nil
		}
	}

	if strings.TrimSpace(h.Message) != "" {
		var props []models.Property
		if err := db.Where("is_active = ?", true).Find(&props).Error; err != nil {
			return nil, err
		}
		msg := strings.ToLower(h.Message)
		for _, p := range props {
			if p.Name != "" && strings.Contains(msg, strings.ToLower(p.Name)) {
				return &p, nil
			}
			for _, tok := range strings.Fields(strings.ToLower(p.Address)) {
				if len(tok) >= 5 && !isAllDigits(tok) && strings.Contains(msg, tok) {
					return &p, nil
				}
			}
		}
	}

	var all []models.Property
	if err := db.Where("is_active = ?", true).Find(&all).Error; err != nil {
		return nil, err
	}
	if len(all) == 1 {
		return &all[0], nil
	}

	return nil, nil
}

func isAllDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
