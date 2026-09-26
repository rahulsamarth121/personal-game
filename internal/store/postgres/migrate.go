package postgres

import (
	"database/sql"

	"github.com/personal-game/personal-game/internal/store/migrations"
)

// Migrate applies the base schema (idempotent: all statements are
// CREATE TABLE IF NOT EXISTS). Run once at control-plane startup when
// PostgreSQL is configured.
func Migrate(db *sql.DB) error {
	_, err := db.Exec(migrations.SQL0001)
	return err
}
