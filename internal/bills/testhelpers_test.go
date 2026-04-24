package bills_test

import (
	"os"
	"path/filepath"
	"time"
)

func readTestdata(name string) ([]byte, error) {
	return os.ReadFile(filepath.Join("testdata", name))
}

func mustDate(s string) time.Time {
	t, _ := time.Parse("2006-01-02", s)
	return t
}
