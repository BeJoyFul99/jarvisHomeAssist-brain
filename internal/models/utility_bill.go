package models

import (
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// UtilityBill — one row per uploaded or manually entered utility bill. See spec §2.2.
type UtilityBill struct {
	ID                   uint           `gorm:"primaryKey" json:"id"`
	PropertyID           uint           `gorm:"not null;index;uniqueIndex:ux_bill_property_hash" json:"property_id"`
	UploadedBy           uint           `gorm:"not null;index" json:"uploaded_by"`
	FilePath             string         `gorm:"size:512" json:"file_path"`
	FileHash             string         `gorm:"size:64;uniqueIndex:ux_bill_property_hash" json:"file_hash"`
	StatementDate        time.Time      `gorm:"index:idx_bill_statement_date" json:"statement_date"`
	DueDate              time.Time      `json:"due_date"`
	BillingPeriodStart   time.Time      `json:"billing_period_start"`
	BillingPeriodEnd     time.Time      `json:"billing_period_end"`
	BillType             string         `gorm:"size:32" json:"bill_type"`
	TotalAmount          float64        `gorm:"type:decimal(12,2)" json:"total_amount"`
	PreviousBalance      float64        `gorm:"type:decimal(12,2)" json:"previous_balance"`
	PaymentsReceived     float64        `gorm:"type:decimal(12,2)" json:"payments_received"`
	BalanceForward       float64        `gorm:"type:decimal(12,2)" json:"balance_forward"`
	LateFees             float64        `gorm:"type:decimal(12,2)" json:"late_fees"`
	Currency             string         `gorm:"size:8;not null;default:CAD" json:"currency"`
	PaymentStatus        string         `gorm:"size:16;not null;default:unpaid" json:"payment_status"`
	PaidDate             *time.Time     `json:"paid_date"`
	PaidAmount           *float64       `gorm:"type:decimal(12,2)" json:"paid_amount"`
	IngestionSource      string         `gorm:"size:16;not null;default:manual_upload" json:"ingestion_source"`
	ExtractionStatus     string         `gorm:"size:16;not null;default:processing;index:idx_bill_extraction_status" json:"extraction_status"`
	ExtractionMethod     *string        `gorm:"size:16" json:"extraction_method"`
	ExtractionModel      *string        `gorm:"size:64" json:"extraction_model"`
	ExtractionConfidence *int           `json:"extraction_confidence"`
	ExtractionError      *string        `gorm:"size:512" json:"extraction_error"`
	RawExtractedData     datatypes.JSON `gorm:"type:jsonb" json:"raw_extracted_data"`
	CreatedAt            time.Time      `json:"created_at"`
	UpdatedAt            time.Time      `json:"updated_at"`
	DeletedAt            gorm.DeletedAt `gorm:"index" json:"-"`
}
