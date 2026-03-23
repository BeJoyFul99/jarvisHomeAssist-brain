package models

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
)

// JSON is a custom GORM type for storing arbitrary JSON in a JSONB column.
type JSON map[string]interface{}

// Value implements the driver.Valuer interface for GORM writes.
func (j JSON) Value() (driver.Value, error) {
	if j == nil {
		return "{}", nil
	}
	return json.Marshal(j)
}

// Scan implements the sql.Scanner interface for GORM reads.
func (j *JSON) Scan(src interface{}) error {
	if src == nil {
		*j = JSON{}
		return nil
	}

	var source []byte
	switch v := src.(type) {
	case []byte:
		source = v
	case string:
		source = []byte(v)
	default:
		return errors.New("unsupported type for JSON scan")
	}

	return json.Unmarshal(source, j)
}
