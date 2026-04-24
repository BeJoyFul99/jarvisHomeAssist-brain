package bills

import (
	"bytes"
	"errors"
	"fmt"
)

// MaxPDFBytes is the upload size limit (spec §3.1).
const MaxPDFBytes = 10 * 1024 * 1024

var (
	ErrNotPDF    = errors.New("not a PDF")
	ErrTooLarge  = errors.New("pdf too large")
	ErrEncrypted = errors.New("pdf is encrypted")
)

// Validate runs cheap upload-time checks. Does NOT parse the full PDF;
// deeper failures surface during extraction.
func Validate(data []byte) error {
	if len(data) > MaxPDFBytes {
		return fmt.Errorf("%w: %d bytes", ErrTooLarge, len(data))
	}
	if !bytes.HasPrefix(data, []byte("%PDF-")) {
		return ErrNotPDF
	}
	if bytes.Contains(data, []byte("/Encrypt")) {
		return ErrEncrypted
	}
	return nil
}
