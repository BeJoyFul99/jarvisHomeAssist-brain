// Package testutil provides shared helpers for Go tests in this repo.
package testutil

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// NewTestDB returns an isolated in-memory SQLite *gorm.DB, closed on test end.
// Each call produces a UUID-scoped DSN so parallel tests and tests in different
// packages don't share schema/state.
func NewTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:testdb-" + uuid.New().String() + "?mode=memory&cache=private"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	return db
}
