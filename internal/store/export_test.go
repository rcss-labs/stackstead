package store

import (
	"database/sql"
	"fmt"
)

// CreateAtVersionForTest builds a database at path using only the first n
// real migrations, the way an older release would have left it.
func CreateAtVersionForTest(path string, n int) error {
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for i := 0; i < n; i++ {
		if _, err := db.Exec(migrations[i]); err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			return err
		}
	}
	return nil
}

// SetLinkFileForTest replaces the hard-link function; it returns a restore func.
func SetLinkFileForTest(f func(oldname, newname string) error) func() {
	old := linkFile
	linkFile = f
	return func() { linkFile = old }
}
