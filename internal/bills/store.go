// Package bills owns the utility-bill extraction and storage domain.
package bills

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ErrBillNotFound is returned when the relative path does not exist.
var ErrBillNotFound = errors.New("bill file not found")

// BillStore persists uploaded bill PDFs and returns a forward-slash relative path
// that can be stored in utility_bills.file_path. Safe for concurrent use.
type BillStore interface {
	Put(propertyID uint, periodYYYYMM, hash string, data []byte) (relPath string, err error)
	Get(relPath string) ([]byte, error)
	Delete(relPath string) error
}

// DiskBillStore stores PDFs at <baseDir>/bills/<propertyID>/<YYYY-MM>/<hash>.pdf
type DiskBillStore struct {
	baseDir string
}

// NewDiskBillStore creates a disk-backed BillStore rooted at baseDir.
func NewDiskBillStore(baseDir string) *DiskBillStore {
	return &DiskBillStore{baseDir: baseDir}
}

func (s *DiskBillStore) relPath(propertyID uint, periodYYYYMM, hash string) string {
	return fmt.Sprintf("bills/%d/%s/%s.pdf", propertyID, periodYYYYMM, hash)
}

func validate(relPath string) error {
	if strings.Contains(relPath, "..") || filepath.IsAbs(relPath) {
		return fmt.Errorf("invalid path: %s", relPath)
	}
	return nil
}

func (s *DiskBillStore) abs(relPath string) string {
	return filepath.Join(s.baseDir, filepath.FromSlash(filepath.Clean(relPath)))
}

// Put writes data and returns the relative path on success.
func (s *DiskBillStore) Put(propertyID uint, periodYYYYMM, hash string, data []byte) (string, error) {
	rel := s.relPath(propertyID, periodYYYYMM, hash)
	abs := s.abs(rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}
	if err := os.WriteFile(abs, data, 0o644); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}
	return rel, nil
}

// Get reads the file at relPath.
func (s *DiskBillStore) Get(relPath string) ([]byte, error) {
	if err := validate(relPath); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(s.abs(relPath))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrBillNotFound
	}
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Delete removes the file at relPath. Missing file is not an error (idempotent).
func (s *DiskBillStore) Delete(relPath string) error {
	if err := validate(relPath); err != nil {
		return err
	}
	err := os.Remove(s.abs(relPath))
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
