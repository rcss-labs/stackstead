package images

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Store persists the image library in SQLite.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// NewStore returns a Store over db; now supplies timestamps.
func NewStore(db *sql.DB, now func() time.Time) *Store {
	return &Store{db: db, now: now}
}

func newID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// stamp returns the current time at the second precision the database keeps.
func (s *Store) stamp() time.Time { return time.Unix(s.now().Unix(), 0) }

func isUniqueViolation(err error) bool {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return false
	}
	c := se.Code()
	return c == sqlite3.SQLITE_CONSTRAINT_UNIQUE || c == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY
}

func encodeArgs(args map[string]string) (string, error) {
	if args == nil {
		args = map[string]string{}
	}
	b, err := json.Marshal(args)
	return string(b), err
}

func decodeArgs(raw string) (map[string]string, error) {
	args := map[string]string{}
	if raw == "" {
		return args, nil
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return nil, fmt.Errorf("decode build args: %w", err)
	}
	if args == nil {
		args = map[string]string{}
	}
	return args, nil
}

// ---- images ----

const imageColumns = `id, slug, name, source, dockerfile_path, github_repo, github_branch, github_path,
	build_args, retention_keep_last, retention_keep_days, created_at, updated_at`

type scanner interface{ Scan(dest ...any) error }

func scanImage(sc scanner) (Image, error) {
	var (
		img              Image
		source, args     string
		created, updated int64
	)
	err := sc.Scan(&img.ID, &img.Slug, &img.Name, &source, &img.DockerfilePath,
		&img.GitHub.Repo, &img.GitHub.Branch, &img.GitHub.Path, &args,
		&img.Retention.KeepLast, &img.Retention.KeepDays, &created, &updated)
	if err != nil {
		return Image{}, err
	}
	img.Source = Source(source)
	img.CreatedAt, img.UpdatedAt = time.Unix(created, 0), time.Unix(updated, 0)
	if img.BuildArgs, err = decodeArgs(args); err != nil {
		return Image{}, err
	}
	return img, nil
}

// CreateImage inserts img, filling its ID and timestamps.
func (s *Store) CreateImage(ctx context.Context, img Image) (Image, error) {
	id, err := newID()
	if err != nil {
		return Image{}, err
	}
	args, err := encodeArgs(img.BuildArgs)
	if err != nil {
		return Image{}, err
	}
	img.ID = id
	img.CreatedAt = s.stamp()
	img.UpdatedAt = img.CreatedAt
	if img.BuildArgs == nil {
		img.BuildArgs = map[string]string{}
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO images (`+imageColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		img.ID, img.Slug, img.Name, string(img.Source), img.DockerfilePath,
		img.GitHub.Repo, img.GitHub.Branch, img.GitHub.Path, args,
		img.Retention.KeepLast, img.Retention.KeepDays, img.CreatedAt.Unix(), img.UpdatedAt.Unix())
	if isUniqueViolation(err) {
		return Image{}, ErrImageExists
	}
	if err != nil {
		return Image{}, fmt.Errorf("create image: %w", err)
	}
	return img, nil
}

func (s *Store) imageWhere(ctx context.Context, column, value string) (Image, error) {
	img, err := scanImage(s.db.QueryRowContext(ctx, `SELECT `+imageColumns+` FROM images WHERE `+column+` = ?`, value))
	if errors.Is(err, sql.ErrNoRows) {
		return Image{}, ErrImageNotFound
	}
	if err != nil {
		return Image{}, fmt.Errorf("read image: %w", err)
	}
	return img, nil
}

// Image returns the image with the given slug.
func (s *Store) Image(ctx context.Context, slug string) (Image, error) {
	return s.imageWhere(ctx, "slug", slug)
}

// ImageByID returns the image with the given ID.
func (s *Store) ImageByID(ctx context.Context, id string) (Image, error) {
	return s.imageWhere(ctx, "id", id)
}

// Images lists all images ordered by name.
func (s *Store) Images(ctx context.Context) ([]Image, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+imageColumns+` FROM images ORDER BY name COLLATE NOCASE, slug`)
	if err != nil {
		return nil, fmt.Errorf("list images: %w", err)
	}
	defer rows.Close()
	out := []Image{}
	for rows.Next() {
		img, err := scanImage(rows)
		if err != nil {
			return nil, fmt.Errorf("list images: %w", err)
		}
		out = append(out, img)
	}
	return out, rows.Err()
}

// UpdateImage rewrites the mutable fields of img (everything except ID, slug
// and creation time) and bumps its update time.
func (s *Store) UpdateImage(ctx context.Context, img Image) error {
	args, err := encodeArgs(img.BuildArgs)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE images SET name=?, source=?, dockerfile_path=?, github_repo=?, github_branch=?,
		github_path=?, build_args=?, retention_keep_last=?, retention_keep_days=?, updated_at=? WHERE id=?`,
		img.Name, string(img.Source), img.DockerfilePath, img.GitHub.Repo, img.GitHub.Branch, img.GitHub.Path,
		args, img.Retention.KeepLast, img.Retention.KeepDays, s.stamp().Unix(), img.ID)
	if err != nil {
		return fmt.Errorf("update image: %w", err)
	}
	return requireRow(res, ErrImageNotFound)
}

// DeleteImage removes an image; its revisions, versions, targets and pushes cascade.
func (s *Store) DeleteImage(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM images WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete image: %w", err)
	}
	return requireRow(res, ErrImageNotFound)
}

func requireRow(res sql.Result, notFound error) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return notFound
	}
	return nil
}

// ---- revisions ----

const revisionColumns = `image_id, number, created_at, summary, manifest`

func scanRevision(sc scanner) (Revision, error) {
	var (
		r       Revision
		created int64
	)
	if err := sc.Scan(&r.ImageID, &r.Number, &created, &r.Summary, &r.ManifestJSON); err != nil {
		return Revision{}, err
	}
	r.CreatedAt = time.Unix(created, 0)
	return r, nil
}

// AddRevision records a new revision numbered one above the image's latest.
func (s *Store) AddRevision(ctx context.Context, imageID, summary, manifestJSON string) (Revision, error) {
	r := Revision{ImageID: imageID, CreatedAt: s.stamp(), Summary: summary, ManifestJSON: manifestJSON}
	err := s.db.QueryRowContext(ctx, `INSERT INTO image_revisions (`+revisionColumns+`)
		SELECT ?, COALESCE(MAX(number), 0) + 1, ?, ?, ? FROM image_revisions WHERE image_id = ?
		RETURNING number`,
		imageID, r.CreatedAt.Unix(), summary, manifestJSON, imageID).Scan(&r.Number)
	if err != nil {
		return Revision{}, fmt.Errorf("add revision: %w", err)
	}
	return r, nil
}

// Revisions lists an image's revisions, newest first.
func (s *Store) Revisions(ctx context.Context, imageID string) ([]Revision, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+revisionColumns+` FROM image_revisions WHERE image_id = ? ORDER BY number DESC`, imageID)
	if err != nil {
		return nil, fmt.Errorf("list revisions: %w", err)
	}
	defer rows.Close()
	out := []Revision{}
	for rows.Next() {
		r, err := scanRevision(rows)
		if err != nil {
			return nil, fmt.Errorf("list revisions: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Revision returns revision n of an image.
func (s *Store) Revision(ctx context.Context, imageID string, n int) (Revision, error) {
	r, err := scanRevision(s.db.QueryRowContext(ctx, `SELECT `+revisionColumns+` FROM image_revisions WHERE image_id = ? AND number = ?`, imageID, n))
	if errors.Is(err, sql.ErrNoRows) {
		return Revision{}, ErrRevisionNotFound
	}
	if err != nil {
		return Revision{}, fmt.Errorf("read revision: %w", err)
	}
	return r, nil
}

// LatestRevision returns the newest revision, and false when there is none.
func (s *Store) LatestRevision(ctx context.Context, imageID string) (Revision, bool, error) {
	r, err := scanRevision(s.db.QueryRowContext(ctx, `SELECT `+revisionColumns+` FROM image_revisions WHERE image_id = ? ORDER BY number DESC LIMIT 1`, imageID))
	if errors.Is(err, sql.ErrNoRows) {
		return Revision{}, false, nil
	}
	if err != nil {
		return Revision{}, false, fmt.Errorf("read latest revision: %w", err)
	}
	return r, true, nil
}

// ---- versions ----

const versionColumns = `id, image_id, tag, status, revision, commit_sha, build_args, arch, image_digest, size,
	started_at, ended_at, error, operation_id`

func scanVersion(sc scanner) (Version, error) {
	var (
		v       Version
		status  string
		args    string
		started int64
		ended   sql.NullInt64
	)
	err := sc.Scan(&v.ID, &v.ImageID, &v.Tag, &status, &v.Revision, &v.Commit, &args, &v.Arch, &v.ImageDigest,
		&v.Size, &started, &ended, &v.Error, &v.OperationID)
	if err != nil {
		return Version{}, err
	}
	v.Status = VersionStatus(status)
	v.StartedAt = time.Unix(started, 0)
	if ended.Valid {
		t := time.Unix(ended.Int64, 0)
		v.EndedAt = &t
	}
	if v.BuildArgs, err = decodeArgs(args); err != nil {
		return Version{}, err
	}
	return v, nil
}

func (s *Store) queryVersions(ctx context.Context, where string, args ...any) ([]Version, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+versionColumns+` FROM image_versions `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("list versions: %w", err)
	}
	defer rows.Close()
	out := []Version{}
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, fmt.Errorf("list versions: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// StartVersion records a new building version. It answers ErrBuildRunning when
// the image already has a building row and ErrTagTaken when a non-failed
// version has the tag.
func (s *Store) StartVersion(ctx context.Context, v Version) (Version, error) {
	id, err := newID()
	if err != nil {
		return Version{}, err
	}
	args, err := encodeArgs(v.BuildArgs)
	if err != nil {
		return Version{}, err
	}
	v.ID, v.Status, v.StartedAt, v.EndedAt = id, VersionBuilding, s.stamp(), nil
	if v.BuildArgs == nil {
		v.BuildArgs = map[string]string{}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Version{}, fmt.Errorf("start version: %w", err)
	}
	defer tx.Rollback()
	var building int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM image_versions WHERE image_id = ? AND status = 'building'`, v.ImageID).Scan(&building); err != nil {
		return Version{}, fmt.Errorf("start version: %w", err)
	}
	if building > 0 {
		return Version{}, ErrBuildRunning
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO image_versions (`+versionColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,NULL,?,?)`,
		v.ID, v.ImageID, v.Tag, string(v.Status), v.Revision, v.Commit, args, v.Arch, v.ImageDigest, v.Size,
		v.StartedAt.Unix(), v.Error, v.OperationID)
	if isUniqueViolation(err) && strings.Contains(err.Error(), "image_versions.tag") {
		return Version{}, ErrTagTaken
	}
	if err != nil {
		return Version{}, fmt.Errorf("start version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Version{}, fmt.Errorf("start version: %w", err)
	}
	return v, nil
}

// FinishVersion records the outcome of a build and stamps its end time.
func (s *Store) FinishVersion(ctx context.Context, id string, r VersionResult) error {
	res, err := s.db.ExecContext(ctx, `UPDATE image_versions SET status=?, arch=?, image_digest=?, size=?, error=?, ended_at=? WHERE id=?`,
		string(r.Status), r.Arch, r.ImageDigest, r.Size, r.Error, s.stamp().Unix(), id)
	if err != nil {
		return fmt.Errorf("finish version: %w", err)
	}
	return requireRow(res, ErrVersionNotFound)
}

// Versions lists an image's versions, newest first.
func (s *Store) Versions(ctx context.Context, imageID string) ([]Version, error) {
	return s.queryVersions(ctx, `WHERE image_id = ? ORDER BY started_at DESC, rowid DESC`, imageID)
}

// Version returns the non-failed version with the tag.
func (s *Store) Version(ctx context.Context, imageID, tag string) (Version, error) {
	v, err := scanVersion(s.db.QueryRowContext(ctx, `SELECT `+versionColumns+` FROM image_versions WHERE image_id = ? AND tag = ? AND status != 'failed'`, imageID, tag))
	if errors.Is(err, sql.ErrNoRows) {
		return Version{}, ErrVersionNotFound
	}
	if err != nil {
		return Version{}, fmt.Errorf("read version: %w", err)
	}
	return v, nil
}

// VersionByID returns the version with the given ID.
func (s *Store) VersionByID(ctx context.Context, id string) (Version, error) {
	v, err := scanVersion(s.db.QueryRowContext(ctx, `SELECT `+versionColumns+` FROM image_versions WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Version{}, ErrVersionNotFound
	}
	if err != nil {
		return Version{}, fmt.Errorf("read version: %w", err)
	}
	return v, nil
}

// DeleteVersion removes a version and its pushes.
func (s *Store) DeleteVersion(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM image_versions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete version: %w", err)
	}
	return requireRow(res, ErrVersionNotFound)
}

// LatestSucceeded returns the newest succeeded version, and false when there is none.
func (s *Store) LatestSucceeded(ctx context.Context, imageID string) (Version, bool, error) {
	v, err := scanVersion(s.db.QueryRowContext(ctx, `SELECT `+versionColumns+` FROM image_versions
		WHERE image_id = ? AND status = 'succeeded' ORDER BY started_at DESC, rowid DESC LIMIT 1`, imageID))
	if errors.Is(err, sql.ErrNoRows) {
		return Version{}, false, nil
	}
	if err != nil {
		return Version{}, false, fmt.Errorf("read latest version: %w", err)
	}
	return v, true, nil
}

// BuildingVersions lists building versions across all images, for restart recovery.
func (s *Store) BuildingVersions(ctx context.Context) ([]Version, error) {
	return s.queryVersions(ctx, `WHERE status = 'building' ORDER BY started_at, rowid`)
}

// ---- push targets and pushes ----

// AddPushTarget inserts t, filling its ID.
func (s *Store) AddPushTarget(ctx context.Context, t PushTarget) (PushTarget, error) {
	id, err := newID()
	if err != nil {
		return PushTarget{}, err
	}
	t.ID = id
	if _, err := s.db.ExecContext(ctx, `INSERT INTO push_targets (id, image_id, registry_id, repository, auto_push) VALUES (?,?,?,?,?)`,
		t.ID, t.ImageID, t.RegistryID, t.Repository, t.AutoPush); err != nil {
		return PushTarget{}, fmt.Errorf("add push target: %w", err)
	}
	return t, nil
}

// PushTargets lists an image's push targets in creation order.
func (s *Store) PushTargets(ctx context.Context, imageID string) ([]PushTarget, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, image_id, registry_id, repository, auto_push FROM push_targets WHERE image_id = ? ORDER BY rowid`, imageID)
	if err != nil {
		return nil, fmt.Errorf("list push targets: %w", err)
	}
	defer rows.Close()
	out := []PushTarget{}
	for rows.Next() {
		var t PushTarget
		if err := rows.Scan(&t.ID, &t.ImageID, &t.RegistryID, &t.Repository, &t.AutoPush); err != nil {
			return nil, fmt.Errorf("list push targets: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// UpdatePushTarget rewrites a target's registry, repository and auto-push flag.
func (s *Store) UpdatePushTarget(ctx context.Context, t PushTarget) error {
	res, err := s.db.ExecContext(ctx, `UPDATE push_targets SET registry_id=?, repository=?, auto_push=? WHERE id=?`,
		t.RegistryID, t.Repository, t.AutoPush, t.ID)
	if err != nil {
		return fmt.Errorf("update push target: %w", err)
	}
	return requireRow(res, ErrTargetNotFound)
}

// DeletePushTarget removes a target and its pushes.
func (s *Store) DeletePushTarget(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM push_targets WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete push target: %w", err)
	}
	return requireRow(res, ErrTargetNotFound)
}

// SetPush inserts or replaces the push of a version to a target, stamping its update time.
func (s *Store) SetPush(ctx context.Context, p Push) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO image_pushes (version_id, target_id, status, digest, error, updated_at)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT(version_id, target_id) DO UPDATE SET status=excluded.status, digest=excluded.digest,
			error=excluded.error, updated_at=excluded.updated_at`,
		p.VersionID, p.TargetID, string(p.Status), p.Digest, p.Error, s.stamp().Unix())
	if err != nil {
		return fmt.Errorf("set push: %w", err)
	}
	return nil
}

// Pushes returns the pushes of the given versions keyed by version ID.
func (s *Store) Pushes(ctx context.Context, versionIDs []string) (map[string][]Push, error) {
	out := map[string][]Push{}
	if len(versionIDs) == 0 {
		return out, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(versionIDs)), ",")
	args := make([]any, len(versionIDs))
	for i, id := range versionIDs {
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, `SELECT version_id, target_id, status, digest, error, updated_at FROM image_pushes
		WHERE version_id IN (`+marks+`) ORDER BY version_id, rowid`, args...)
	if err != nil {
		return nil, fmt.Errorf("list pushes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			p       Push
			status  string
			updated int64
		)
		if err := rows.Scan(&p.VersionID, &p.TargetID, &status, &p.Digest, &p.Error, &updated); err != nil {
			return nil, fmt.Errorf("list pushes: %w", err)
		}
		p.Status, p.UpdatedAt = PushStatus(status), time.Unix(updated, 0)
		out[p.VersionID] = append(out[p.VersionID], p)
	}
	return out, rows.Err()
}
