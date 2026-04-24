package bills_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"jarvishomeassist-brain/internal/bills"
)

func TestExtractText_EmptyInput(t *testing.T) {
	_, err := bills.ExtractText([]byte{})
	require.Error(t, err)
}

func TestExtractText_InvalidPDF(t *testing.T) {
	_, err := bills.ExtractText([]byte("not a pdf"))
	require.Error(t, err)
}
