package bills_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"jarvishomeassist-brain/internal/bills"
)

func TestRasterize_EmptyData(t *testing.T) {
	_, err := bills.Rasterize([]byte{})
	require.Error(t, err)
}
