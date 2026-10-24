package store

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/pp/db-compare/internal/dto"
)

func (s *Store) initSnapshots() error {
	_, err := s.db.Exec(`
 CREATE TABLE IF NOT EXISTS schema_blobs(hash TEXT PRIMARY KEY, data BLOB NOT NULL);
 CREATE TABLE IF NOT EXISTS schema_snapshots(id TEXT PRIMARY KEY, task_id TEXT NOT NULL, connection_id TEXT NOT NULL, database_name TEXT NOT NULL, captured_at TEXT NOT NULL, hash TEXT NOT NULL, pinned INTEGER NOT NULL DEFAULT 0, payload TEXT NOT NULL);
 CREATE INDEX IF NOT EXISTS schema_snapshot_database ON schema_snapshots(connection_id,database_name,captured_at);
 CREATE TABLE IF NOT EXISTS snapshot_tasks(id TEXT PRIMARY KEY, next_run TEXT NOT NULL, enabled INTEGER NOT NULL, lease_until TEXT NOT NULL DEFAULT '', payload TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS snapshot_runs(id TEXT PRIMARY KEY, started_at TEXT NOT NULL, payload TEXT NOT NULL);
 `)
	return err
}
func (s *Store) PutSnapshot(rec dto.SnapshotRecord, archive dto.SchemaArchive) (dto.SnapshotRecord, error) {
	return s.putSnapshot(rec, archive, nil)
}

// PutSnapshotIfChanged reuses the latest version for this connection/database.
// Comparison and insertion share a writer transaction so UI/background workers
// cannot both create a new version of the same structure.
func (s *Store) PutSnapshotIfChanged(rec dto.SnapshotRecord, archive dto.SchemaArchive, sameStructure func(dto.SchemaArchive, dto.SchemaArchive) (bool, error)) (dto.SnapshotRecord, error) {
	return s.putSnapshot(rec, archive, sameStructure)
}

func (s *Store) putSnapshot(rec dto.SnapshotRecord, archive dto.SchemaArchive, sameStructure func(dto.SchemaArchive, dto.SchemaArchive) (bool, error)) (dto.SnapshotRecord, error) {
	raw, err := json.Marshal(archive)
	if err != nil {
		return rec, err
	}
	if len(raw) > 64<<20 {
		return rec, fmt.Errorf("数据库结构超过 64 MB，未保存快照")
	}
	sum := sha256.Sum256(raw)
	rec.Hash = hex.EncodeToString(sum[:])
	rec.ID = uuid.NewString()
	rec.TableCount = len(archive.Tables)
	rec.Warnings = archive.Warnings
	rec.LastCheckedAt = rec.CapturedAt
	tx, err := s.db.Begin()
	if err != nil {
		return rec, err
	}
	defer tx.Rollback()
	// Acquire SQLite's writer lock before reading the latest version, including
	// when the snapshot table is empty. No rows are changed by this statement.
	if _, err = tx.Exec(`UPDATE schema_snapshots SET id=id WHERE 0`); err != nil {
		return rec, err
	}
	var previousPayload string
	err = tx.QueryRow(`SELECT payload FROM schema_snapshots WHERE connection_id=? AND database_name=? ORDER BY captured_at DESC,rowid DESC LIMIT 1`, rec.ConnectionID, rec.Database).Scan(&previousPayload)
	if err != nil && err != sql.ErrNoRows {
		return rec, err
	}
	if err == nil {
		var previous dto.SnapshotRecord
		if err = json.Unmarshal([]byte(previousPayload), &previous); err != nil {
			return rec, err
		}
		rec.Unchanged = previous.Hash == rec.Hash
		if sameStructure != nil {
			if !rec.Unchanged {
				var data []byte
				if err = tx.QueryRow(`SELECT data FROM schema_blobs WHERE hash=?`, previous.Hash).Scan(&data); err != nil {
					return rec, err
				}
				doc, err := decodeSnapshot(previousPayload, data)
				if err != nil {
					return rec, err
				}
				rec.Unchanged, err = sameStructure(doc.Archive, archive)
				if err != nil {
					return rec, err
				}
			}
			if rec.Unchanged {
				// Advance the successful check time without changing the version's
				// creation time, content, task ownership, notes or pinned state.
				checked, err := time.Parse(time.RFC3339Nano, rec.CapturedAt)
				if err != nil {
					return rec, err
				}
				lastChecked := previous.LastCheckedAt
				if lastChecked == "" {
					lastChecked = previous.CapturedAt
				}
				last, err := time.Parse(time.RFC3339Nano, lastChecked)
				if err != nil {
					return rec, err
				}
				previous.LastCheckedAt = lastChecked
				if checked.After(last) {
					previous.LastCheckedAt = rec.CapturedAt
				}
				payload, err := json.Marshal(previous)
				if err != nil {
					return rec, err
				}
				if _, err = tx.Exec(`UPDATE schema_snapshots SET payload=? WHERE id=?`, string(payload), previous.ID); err != nil {
					return rec, err
				}
				previous.Unchanged = true
				return previous, tx.Commit()
			}
		}
	}
	var compressed bytes.Buffer
	z := gzip.NewWriter(&compressed)
	if _, err = z.Write(raw); err != nil {
		return rec, err
	}
	if err = z.Close(); err != nil {
		return rec, err
	}
	payload, err := json.Marshal(rec)
	if err != nil {
		return rec, err
	}
	if _, err = tx.Exec(`INSERT OR IGNORE INTO schema_blobs(hash,data) VALUES(?,?)`, rec.Hash, compressed.Bytes()); err != nil {
		return rec, err
	}
	if _, err = tx.Exec(`INSERT INTO schema_snapshots(id,task_id,connection_id,database_name,captured_at,hash,pinned,payload) VALUES(?,?,?,?,?,?,?,?)`, rec.ID, rec.TaskID, rec.ConnectionID, rec.Database, rec.CapturedAt, rec.Hash, rec.Pinned, string(payload)); err != nil {
		return rec, err
	}
	return rec, tx.Commit()
}
func (s *Store) Snapshot(id string) (dto.SnapshotDocument, error) {
	var out dto.SnapshotDocument
	var payload string
	var data []byte
	if err := s.db.QueryRow(`SELECT s.payload,b.data FROM schema_snapshots s JOIN schema_blobs b ON b.hash=s.hash WHERE s.id=?`, id).Scan(&payload, &data); err != nil {
		return out, err
	}
	return decodeSnapshot(payload, data)
}

func decodeSnapshot(payload string, data []byte) (dto.SnapshotDocument, error) {
	var out dto.SnapshotDocument
	if err := json.Unmarshal([]byte(payload), &out.Record); err != nil {
		return out, err
	}
	z, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return out, err
	}
	defer z.Close()
	raw, err := io.ReadAll(io.LimitReader(z, (64<<20)+1))
	if err != nil {
		return out, err
	}
	if len(raw) > 64<<20 {
		return out, fmt.Errorf("快照解压内容超过 64 MB")
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != out.Record.Hash {
		return out, fmt.Errorf("快照内容校验失败")
	}
	err = json.Unmarshal(raw, &out.Archive)
	return out, err
}
func (s *Store) SnapshotRecords() ([]dto.SnapshotRecord, error) {
	rows, err := s.db.Query(`SELECT payload FROM schema_snapshots ORDER BY captured_at DESC,rowid DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []dto.SnapshotRecord{}
	for rows.Next() {
		var p string
		var v dto.SnapshotRecord
		if err = rows.Scan(&p); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(p), &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) AnnotateSnapshot(id, note string, pinned bool) error {
	doc, err := s.Snapshot(id)
	if err != nil {
		return err
	}
	doc.Record.Note = note
	doc.Record.Pinned = pinned
	p, err := json.Marshal(doc.Record)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE schema_snapshots SET payload=?,pinned=? WHERE id=?`, string(p), pinned, id)
	return err
}
func (s *Store) DeleteSnapshot(id string) error {
	_, err := s.db.Exec(`DELETE FROM schema_snapshots WHERE id=?`, id)
	if err != nil {
		return err
	}
	return s.gcSnapshotBlobs()
}
func (s *Store) gcSnapshotBlobs() error {
	_, err := s.db.Exec(`DELETE FROM schema_blobs WHERE hash NOT IN (SELECT hash FROM schema_snapshots)`)
	return err
}
func (s *Store) PruneSnapshots(taskID string, days int) error {
	if days < 1 {
		return nil
	}
	// Keep at least the most recent snapshot of each database, even across long outages.
	_, err := s.db.Exec(`DELETE FROM schema_snapshots WHERE task_id=? AND pinned=0 AND captured_at<? AND id NOT IN (SELECT id FROM schema_snapshots s WHERE NOT EXISTS (SELECT 1 FROM schema_snapshots n WHERE n.connection_id=s.connection_id AND n.database_name=s.database_name AND (n.captured_at>s.captured_at OR (n.captured_at=s.captured_at AND n.rowid>s.rowid))))`, taskID, time.Now().UTC().AddDate(0, 0, -days).Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	return s.gcSnapshotBlobs()
}
func (s *Store) SnapshotTasks() ([]dto.SnapshotTask, error) {
	rows, err := s.db.Query(`SELECT payload,next_run,enabled FROM snapshot_tasks ORDER BY rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []dto.SnapshotTask{}
	for rows.Next() {
		var p string
		var t dto.SnapshotTask
		var next string
		var enabled bool
		if err = rows.Scan(&p, &next, &enabled); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(p), &t); err != nil {
			return nil, err
		}
		t.NextRun = next
		t.Enabled = enabled
		out = append(out, t)
	}
	return out, rows.Err()
}
func (s *Store) PutSnapshotTask(t dto.SnapshotTask) (dto.SnapshotTask, error) {
	if t.ID == "" {
		t.ID = uuid.NewString()
	}
	p, err := json.Marshal(t)
	if err != nil {
		return t, err
	}
	res, err := s.db.Exec(`INSERT INTO snapshot_tasks(id,next_run,enabled,payload) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET next_run=excluded.next_run,enabled=excluded.enabled,payload=excluded.payload WHERE snapshot_tasks.lease_until<?`, t.ID, t.NextRun, t.Enabled, string(p), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return t, err
	}
	n, err := res.RowsAffected()
	if err == nil && n == 0 {
		err = fmt.Errorf("任务正在执行，暂不能修改")
	}
	return t, err
}
func (s *Store) DeleteSnapshotTask(id string) error {
	res, err := s.db.Exec(`DELETE FROM snapshot_tasks WHERE id=? AND lease_until<?`, id, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n == 0 {
		err = fmt.Errorf("任务不存在或正在执行，暂不能删除")
	}
	return err
}

// Claim is atomic across the UI process and launchd workers. Lease is renewed per database.
func (s *Store) ClaimSnapshotTask(id, next string, dueOnly bool) (bool, error) {
	now := time.Now().UTC()
	query := `UPDATE snapshot_tasks SET lease_until=?`
	deadline := now.Add(10 * time.Minute).Format(time.RFC3339Nano)
	if _, loaded := s.snapshotClaims.LoadOrStore(id, deadline); loaded {
		return false, nil
	}
	claimed := false
	defer func() {
		if !claimed {
			s.snapshotClaims.Delete(id)
		}
	}()
	args := []any{deadline}
	if dueOnly {
		query += `,next_run=?`
		args = append(args, next)
	}
	query += ` WHERE id=? AND lease_until<?`
	args = append(args, id, now.Format(time.RFC3339Nano))
	if dueOnly {
		query += ` AND enabled=1 AND next_run<=?`
		args = append(args, now.Format(time.RFC3339Nano))
	}
	res, err := s.db.Exec(query, args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	claimed = n == 1 && err == nil
	return claimed, err
}
func (s *Store) ReleaseSnapshotTask(id string) {
	old, ok := s.snapshotClaims.LoadAndDelete(id)
	if !ok {
		return
	}
	_, _ = s.db.Exec(`UPDATE snapshot_tasks SET lease_until='' WHERE id=? AND lease_until=?`, id, old)
}
func (s *Store) RenewSnapshotTask(id string) error {
	old, ok := s.snapshotClaims.Load(id)
	if !ok {
		return fmt.Errorf("任务没有执行锁")
	}
	deadline := time.Now().UTC().Add(10 * time.Minute).Format(time.RFC3339Nano)
	res, err := s.db.Exec(`UPDATE snapshot_tasks SET lease_until=? WHERE id=? AND lease_until=?`, deadline, id, old)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("任务执行锁已失效，请查看最近执行记录")
	}
	s.snapshotClaims.Store(id, deadline)
	return nil
}
func (s *Store) PutSnapshotRun(r dto.SnapshotRun) error {
	p, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO snapshot_runs(id,started_at,payload) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload`, r.ID, r.StartedAt, string(p))
	return err
}
func (s *Store) SnapshotRuns() ([]dto.SnapshotRun, error) {
	rows, err := s.db.Query(`SELECT payload FROM snapshot_runs ORDER BY started_at DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []dto.SnapshotRun{}
	for rows.Next() {
		var p string
		var r dto.SnapshotRun
		if err = rows.Scan(&p); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(p), &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ImportSnapshots commits the complete validated package or none of it.
func (s *Store) ImportSnapshots(docs []dto.SnapshotDocument) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	connIDs := map[string]string{}
	for _, doc := range docs {
		rec := doc.Record
		key := rec.ConnectionID
		if connIDs[key] == "" {
			connIDs[key] = "imported:" + uuid.NewString()
		}
		rec.ConnectionID = connIDs[key]
		rec.ConnectionName += "（导入）"
		rec.TaskID = ""
		rec.ID = uuid.NewString()
		rec.Unchanged = false
		raw, err := json.Marshal(doc.Archive)
		if err != nil {
			return 0, err
		}
		sum := sha256.Sum256(raw)
		rec.Hash = hex.EncodeToString(sum[:])
		rec.TableCount = len(doc.Archive.Tables)
		rec.Warnings = doc.Archive.Warnings
		var buf bytes.Buffer
		z := gzip.NewWriter(&buf)
		if _, err = z.Write(raw); err != nil {
			return 0, err
		}
		if err = z.Close(); err != nil {
			return 0, err
		}
		p, err := json.Marshal(rec)
		if err != nil {
			return 0, err
		}
		if _, err = tx.Exec(`INSERT OR IGNORE INTO schema_blobs(hash,data) VALUES(?,?)`, rec.Hash, buf.Bytes()); err != nil {
			return 0, err
		}
		if _, err = tx.Exec(`INSERT INTO schema_snapshots(id,task_id,connection_id,database_name,captured_at,hash,pinned,payload) VALUES(?,?,?,?,?,?,?,?)`, rec.ID, "", rec.ConnectionID, rec.Database, rec.CapturedAt, rec.Hash, rec.Pinned, string(p)); err != nil {
			return 0, err
		}
	}
	return len(docs), tx.Commit()
}
