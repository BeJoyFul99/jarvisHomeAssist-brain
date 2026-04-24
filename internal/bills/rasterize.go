package bills

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
)

// Rasterize converts each page of a PDF to a PNG in order. Requires `pdftoppm`
// (poppler-utils) on PATH. On Docker, install via `apt-get install poppler-utils`.
func Rasterize(data []byte) ([][]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("empty pdf data")
	}
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		return nil, fmt.Errorf("pdftoppm not found on PATH: %w", err)
	}
	dir, err := os.MkdirTemp("", "bill-raster-*")
	if err != nil {
		return nil, fmt.Errorf("tempdir: %w", err)
	}
	defer os.RemoveAll(dir)

	pdfPath := filepath.Join(dir, "in.pdf")
	if err := os.WriteFile(pdfPath, data, 0o600); err != nil {
		return nil, fmt.Errorf("write tempfile: %w", err)
	}
	outPrefix := filepath.Join(dir, "page")

	cmd := exec.Command("pdftoppm", "-png", "-r", "150", pdfPath, outPrefix)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("pdftoppm: %w (%s)", err, stderr.String())
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read outputs: %w", err)
	}
	var pngs []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".png" {
			pngs = append(pngs, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(pngs)

	pages := make([][]byte, 0, len(pngs))
	for _, p := range pngs {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", p, err)
		}
		pages = append(pages, b)
	}
	return pages, nil
}
