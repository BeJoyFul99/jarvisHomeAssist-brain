package bills_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"jarvishomeassist-brain/internal/bills"
)

func TestVisionExtract_SendsImageAndParses(t *testing.T) {
	var gotAuth string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": `{"account_number":"987654321","statement_date":"2026-04-01","due_date":"2026-04-21","bill_type":"REGULAR","total_amount":92.78,"currency":"CAD","line_items":[],"meters":[]}`,
		})
	}))
	defer srv.Close()

	client := bills.NewVisionClient(srv.URL, "test-secret", 10*time.Second)
	p, err := client.Extract(context.Background(), [][]byte{[]byte("fake-png")})
	require.NoError(t, err)
	require.Equal(t, "Bearer test-secret", gotAuth)
	require.NotEmpty(t, gotBody)
	require.Equal(t, "987654321", p.AccountNumber)
	require.InDelta(t, 92.78, p.TotalAmount, 0.01)
}

func TestVisionExtract_NoPages_Errors(t *testing.T) {
	client := bills.NewVisionClient("http://x", "s", time.Second)
	_, err := client.Extract(context.Background(), nil)
	require.Error(t, err)
}

func TestVisionExtract_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()
	client := bills.NewVisionClient(srv.URL, "s", 50*time.Millisecond)
	_, err := client.Extract(context.Background(), [][]byte{[]byte("png")})
	require.Error(t, err)
}
