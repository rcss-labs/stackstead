package store_test

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rcss-labs/stackstead/internal/store"
)

func TestContainerTransitionsMigrationFromVersion5(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v5.db")
	if err := store.CreateAtVersionForTest(path, 5); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO users(username,password_hash,created_at) VALUES('admin','hash',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO action_log(ts,username,kind,scope,target,result) VALUES(1,'admin','storage.delete','host','volume/old','deleted')`); err != nil {
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
	if version != 8 || store.SupportedSchema() != 8 {
		t.Fatalf("schema = %d supported = %d, want 8", version, store.SupportedSchema())
	}
	if n := count(t, db, "users"); n != 1 {
		t.Fatal("migration lost users")
	}
	if n := count(t, db, "action_log"); n != 1 {
		t.Fatal("migration lost action_log rows")
	}
	if _, err = db.Exec(`INSERT INTO container_transitions(ts,project,service,container,state,exit_code) VALUES(1,'p','web','p-web-1','exited',137)`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO container_transitions(ts,project,service,container,state,exit_code) VALUES(2,'p','web','p-web-1','running',NULL)`); err != nil {
		t.Fatalf("insert with NULL exit_code: %v", err)
	}
	_, err = db.Exec(`INSERT INTO container_transitions(ts,project,service,container,state) VALUES(3,'p','web','p-web-1','bogus')`)
	if err == nil || !strings.Contains(err.Error(), "CHECK") {
		t.Errorf("insert with unknown state: err = %v, want CHECK failure", err)
	}
	rows, err := db.Query(`SELECT name FROM pragma_index_info('container_transitions_project_ts') ORDER BY seqno`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, col)
	}
	if got := strings.Join(cols, ","); got != "project,ts" {
		t.Errorf("index columns = %q, want project,ts", got)
	}
}
