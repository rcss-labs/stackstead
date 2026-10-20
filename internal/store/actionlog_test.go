package store_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/rcss-labs/stackstead/internal/store"
)

func TestActionLogMigrationFromVersion4(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v4.db")
	if err := store.CreateAtVersionForTest(path, 4); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO users(username,password_hash,created_at) VALUES('admin','hash',0)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 7 || store.SupportedSchema() != 7 {
		t.Fatalf("schema = %d supported = %d, want 7", version, store.SupportedSchema())
	}
	if n := count(t, db, "users"); n != 1 {
		t.Fatal("migration lost users")
	}
	if _, err = db.Exec(`INSERT INTO action_log(ts,username,kind,scope,target,result) VALUES(1,'admin','storage.delete','host','volume/old','deleted')`); err != nil {
		t.Fatal(err)
	}
	var detail, operation string
	var bytes sql.NullInt64
	if err = db.QueryRow(`SELECT detail,operation_id,bytes FROM action_log`).Scan(&detail, &operation, &bytes); err != nil {
		t.Fatal(err)
	}
	if detail != "" || operation != "" || bytes.Valid {
		t.Fatal("unexpected optional field defaults")
	}
	for name, cols := range map[string]string{"action_log_ts": "ts", "action_log_kind_ts": "kind,ts"} {
		rows, err := db.Query(`SELECT name FROM pragma_index_info(?) ORDER BY seqno`, name)
		if err != nil {
			t.Fatal(err)
		}
		got := ""
		for rows.Next() {
			var col string
			if err := rows.Scan(&col); err != nil {
				t.Fatal(err)
			}
			if got != "" {
				got += ","
			}
			got += col
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		if got != cols {
			t.Errorf("index %s = %q want %q", name, got, cols)
		}
	}
}
