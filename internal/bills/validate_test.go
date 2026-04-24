package bills_test

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
	"jarvishomeassist-brain/internal/bills"
)

func TestValidate_AcceptsValidPDF(t *testing.T) {
	data := append([]byte("%PDF-1.4\n"), bytes.Repeat([]byte{0x00}, 1024)...)
	require.NoError(t, bills.Validate(data))
}

func TestValidate_RejectsNonPDFMagic(t *testing.T) {
	data := []byte("\x89PNG\r\n\x1a\n")
	err := bills.Validate(data)
	require.ErrorIs(t, err, bills.ErrNotPDF)
}

func TestValidate_RejectsOversize(t *testing.T) {
	data := append([]byte("%PDF-1.4\n"), bytes.Repeat([]byte{0x00}, 11*1024*1024)...)
	err := bills.Validate(data)
	require.ErrorIs(t, err, bills.ErrTooLarge)
}

func TestValidate_RejectsEncrypted(t *testing.T) {
	data := []byte("%PDF-1.4\n1 0 obj\n<< /Encrypt 2 0 R >>\nendobj\n")
	err := bills.Validate(data)
	require.ErrorIs(t, err, bills.ErrEncrypted)
}
