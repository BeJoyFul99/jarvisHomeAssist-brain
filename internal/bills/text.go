package bills

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/ledongthuc/pdf"
)

// ExtractText returns the concatenated plain text across all pages of a PDF.
// Errors if the PDF is empty, malformed, or unreadable.
func ExtractText(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("empty pdf data")
	}
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("pdf reader: %w", err)
	}
	reader, err := r.GetPlainText()
	if err != nil {
		return "", fmt.Errorf("get plain text: %w", err)
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, reader); err != nil {
		return "", fmt.Errorf("read text: %w", err)
	}
	return buf.String(), nil
}
