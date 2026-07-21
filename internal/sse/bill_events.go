package sse

// Bill lifecycle SSE event types — colon-separated to match the rest of the hub.
const (
	EventBillExtractionStarted   = "bill:extraction:started"
	EventBillExtractionProgress  = "bill:extraction:progress"
	EventBillExtractionCompleted = "bill:extraction:completed"
	EventBillExtractionFailed    = "bill:extraction:failed"
	EventBillImported            = "bill:imported"
)

// BillExtractionEvent is the payload for bill:extraction:* events.
type BillExtractionEvent struct {
	BillID      uint   `json:"bill_id"`
	Status      string `json:"status,omitempty"`
	Method      string `json:"method,omitempty"`
	Confidence  int    `json:"confidence,omitempty"`
	Error       string `json:"error,omitempty"`
	NeedsReview bool   `json:"needs_review,omitempty"`
	Note        string `json:"note,omitempty"`
}
