package bills_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"jarvishomeassist-brain/internal/bills"
)

func TestDiskBillStore_PutGetDelete(t *testing.T) {
	tmp := t.TempDir()
	store := bills.NewDiskBillStore(tmp)

	data := []byte("%PDF-1.4 sample")
	path, err := store.Put(42, "2026-04", "deadbeef", data)
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(tmp, "bills", "42", "2026-04", "deadbeef.pdf"))
	require.Equal(t, "bills/42/2026-04/deadbeef.pdf", path)

	got, err := store.Get(path)
	require.NoError(t, err)
	require.True(t, bytes.Equal(data, got))

	require.NoError(t, store.Delete(path))
	_, err = os.Stat(filepath.Join(tmp, path))
	require.True(t, os.IsNotExist(err))
}

func TestDiskBillStore_Get_NotFound(t *testing.T) {
	store := bills.NewDiskBillStore(t.TempDir())
	_, err := store.Get("bills/1/2026-04/missing.pdf")
	require.ErrorIs(t, err, bills.ErrBillNotFound)
}

func TestDiskBillStore_Rejects_Traversal(t *testing.T) {
	store := bills.NewDiskBillStore(t.TempDir())
	_, err := store.Get("../etc/passwd")
	require.Error(t, err)
}
