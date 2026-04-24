package bills

import "context"

// VisionExtractor abstracts the vision fallback so the orchestrator can be tested
// without hitting a real HTTP endpoint. *VisionClient satisfies this interface.
type VisionExtractor interface {
	Extract(ctx context.Context, pages [][]byte) (ParsedBill, error)
}

// Extractor orchestrates the full pipeline. Function-typed fields allow test stubs.
type Extractor struct {
	ExtractText func(data []byte) (string, error)
	Rasterize   func(data []byte) ([][]byte, error)
	Vision      VisionExtractor
}

// ExtractResult mirrors the columns the orchestrator will write back to
// utility_bills when Phase 5 wires it up.
type ExtractResult struct {
	Parsed     ParsedBill
	Status     string // "completed" | "needs_review" | "failed"
	Method     string // "structured" | "vision_fallback" | ""
	Confidence int
	Error      string
}

// Extract runs validate → text → parse → score. On low structured confidence
// it falls through to vision. Always returns nil error; Status encodes outcome.
func (e *Extractor) Extract(ctx context.Context, data []byte) (ExtractResult, error) {
	if err := Validate(data); err != nil {
		return ExtractResult{Status: "failed", Error: err.Error()}, nil
	}

	text, err := e.ExtractText(data)
	if err != nil {
		return e.visionPath(ctx, data)
	}

	parsed, _ := ParsePowerStream(text)
	score := ScoreConfidence(parsed)

	if score >= 70 {
		return ExtractResult{Parsed: parsed, Status: "completed", Method: "structured", Confidence: score}, nil
	}
	if score >= 50 {
		return ExtractResult{Parsed: parsed, Status: "needs_review", Method: "structured", Confidence: score}, nil
	}
	return e.visionPath(ctx, data)
}

func (e *Extractor) visionPath(ctx context.Context, data []byte) (ExtractResult, error) {
	pages, err := e.Rasterize(data)
	if err != nil {
		return ExtractResult{Status: "needs_review", Error: "rasterize: " + err.Error()}, nil
	}
	if e.Vision == nil {
		return ExtractResult{Status: "needs_review", Error: "no vision client"}, nil
	}
	parsed, err := e.Vision.Extract(ctx, pages)
	if err != nil {
		return ExtractResult{Status: "needs_review", Error: "vision: " + err.Error()}, nil
	}
	score := ScoreConfidence(parsed)
	status := "completed"
	if score < 70 {
		status = "needs_review"
	}
	return ExtractResult{Parsed: parsed, Status: status, Method: "vision_fallback", Confidence: score}, nil
}
