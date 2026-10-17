package update

import (
	"context"
	"errors"
	"github.com/rcss-labs/stackstead/internal/dbmaint"
	"github.com/rcss-labs/stackstead/internal/store"
	"path/filepath"
	"testing"
)

// This fake edge executes real maintenance as the test's own UID. Production
// runtime never calls dbmaint in-process and requires the trusted child role.
type dataRuntime struct {
	*transactionRuntime
	dir, backup string
	schema      int
}

func (r *dataRuntime) Snapshot(ctx context.Context, p Plan) ([]DBSnapshot, error) {
	if e := r.event("snapshot", PhaseStopped); e != nil {
		return nil, e
	}
	schema, digest, e := dbmaint.Snapshot(ctx, r.dir, r.backup)
	return []DBSnapshot{{InstanceID: "app", Path: r.backup, SHA256: digest, ConfigSHA256: "config", Schema: schema}}, e
}
func (r *dataRuntime) StartProbation(ctx context.Context, p Plan, b Probation) error {
	if e := r.transactionRuntime.StartProbation(ctx, p, b); e != nil {
		return e
	}
	if b.Expected == p.Target.Identity {
		db, e := store.Open(filepath.Join(r.dir, "stackstead.db"))
		if e != nil {
			return e
		}
		_, e = db.Exec("UPDATE settings SET value='probation' WHERE key='row'")
		db.Close()
		return e
	}
	return nil
}
func (r *dataRuntime) Ready(ctx context.Context, p Plan, b Probation) ([]Readiness, error) {
	rows, e := r.transactionRuntime.Ready(ctx, p, b)
	if b.Expected == p.Target.Identity {
		return nil, errors.New("target readiness failed")
	}
	for i := range rows {
		rows[i].Schema = r.schema
	}
	return rows, e
}
func (r *dataRuntime) Restore(ctx context.Context, p Plan, s []DBSnapshot) error {
	if e := r.event("restore", PhaseRollingBack); e != nil {
		return e
	}
	return dbmaint.Restore(ctx, r.dir, s[0].Path, filepath.Join(filepath.Dir(r.backup), "failed"))
}
func TestProbationRollbackRestoresSameSchemaData(t *testing.T) {
	c, r, j := transactionFixture(t)
	dir := t.TempDir()
	db, e := store.Open(filepath.Join(dir, "stackstead.db"))
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec("INSERT INTO settings(key,value) VALUES ('row','old-final')")
	if e != nil {
		t.Fatal(e)
	}
	db.Close()
	schema := store.SupportedSchema()
	j.Plan.Current.Manifest.TargetSchema = schema
	j.Plan.Target.Manifest.TargetSchema = schema
	_ = c.journal.Save(j)
	c.runtime = &dataRuntime{transactionRuntime: r, dir: dir, backup: filepath.Join(t.TempDir(), "snapshot.db"), schema: schema}
	if e = c.Recover(context.Background(), "install"); e != nil {
		t.Fatal(e)
	}
	if len(r.tickets) != 3 {
		t.Fatalf("missing prequiesce/target/rollback tickets: %+v", r.tickets)
	}
	for i := range r.tickets {
		if r.tickets[i].JobID != j.ID {
			t.Fatal("rollback changed job identity")
		}
		for k := 0; k < i; k++ {
			if r.tickets[i].Nonce == r.tickets[k].Nonce {
				t.Fatal("reused boot nonce")
			}
		}
	}
	db, e = store.Open(filepath.Join(dir, "stackstead.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	var value string
	if e = db.QueryRow("SELECT value FROM settings WHERE key='row'").Scan(&value); e != nil {
		t.Fatal(e)
	}
	if value != "old-final" {
		t.Fatalf("same schema restored wrong rows: %q", value)
	}
}
