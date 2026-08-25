package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"

	"github.com/pp/db-compare/internal/dto"
)

// Store persists connections, profiles, and audit logs in SQLite.
type Store struct {
	db       *sql.DB
	keyring  SecretStore
	appName  string
}

// SecretStore abstracts OS keychain / in-memory fallback.
type SecretStore interface {
	Set(service, user, password string) error
	Get(service, user string) (string, error)
	Delete(service, user string) error
}

// Open creates or opens the local SQLite database under the user config dir.
func Open(dir string, secrets SecretStore) (*Store, error) {
	if dir == "" {
		home, err := os.UserConfigDir()
		if err != nil {
			home, _ = os.UserHomeDir()
		}
		dir = filepath.Join(home, "dbcompare")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "dbcompare.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, keyring: secrets, appName: "dbcompare"}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS connections (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  host TEXT NOT NULL,
  port INTEGER NOT NULL,
  user TEXT NOT NULL,
  database_name TEXT NOT NULL,
  params TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS profiles (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  description TEXT,
  source_id TEXT NOT NULL,
  target_id TEXT NOT NULL,
  tables_json TEXT,
  normalize_json TEXT,
  allow_drop INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS audit_log (
  id TEXT PRIMARY KEY,
  at TEXT NOT NULL,
  action TEXT NOT NULL,
  source_dsn TEXT,
  target_dsn TEXT,
  sql_text TEXT,
  ok INTEGER NOT NULL,
  error TEXT,
  duration_ms INTEGER
);
`)
	if err != nil {
		return err
	}
	return s.migrateProfiles()
}

func (s *Store) migrateProfiles() error {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(1) FROM pragma_table_info('profiles') WHERE name='source_database'`).Scan(&n)
	if n == 0 {
		if _, err := s.db.Exec(`ALTER TABLE profiles ADD COLUMN source_database TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	_ = s.db.QueryRow(`SELECT COUNT(1) FROM pragma_table_info('profiles') WHERE name='target_database'`).Scan(&n)
	if n == 0 {
		if _, err := s.db.Exec(`ALTER TABLE profiles ADD COLUMN target_database TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) secretKey(connID string) (service, user string) {
	return s.appName, "conn:" + connID
}

// SaveConnection upserts a connection; password goes to keyring when non-empty.
func (s *Store) SaveConnection(c dto.ConnConfig) (dto.ConnConfig, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	if c.ID == "" {
		c.ID = uuid.NewString()
	}
	var exists int
	_ = s.db.QueryRow(`SELECT COUNT(1) FROM connections WHERE id=?`, c.ID).Scan(&exists)
	if exists == 0 {
		_, err := s.db.Exec(`INSERT INTO connections(id,name,host,port,user,database_name,params,created_at,updated_at)
VALUES(?,?,?,?,?,?,?,?,?)`, c.ID, c.Name, c.Host, c.Port, c.User, c.Database, c.Params, now, now)
		if err != nil {
			return c, err
		}
	} else {
		_, err := s.db.Exec(`UPDATE connections SET name=?,host=?,port=?,user=?,database_name=?,params=?,updated_at=? WHERE id=?`,
			c.Name, c.Host, c.Port, c.User, c.Database, c.Params, now, c.ID)
		if err != nil {
			return c, err
		}
	}
	if c.Password != "" && s.keyring != nil {
		svc, user := s.secretKey(c.ID)
		if err := s.keyring.Set(svc, user, c.Password); err != nil {
			return c, fmt.Errorf("store password: %w", err)
		}
	}
	c.Password = ""
	return c, nil
}

// ListConnections returns saved connections without passwords.
func (s *Store) ListConnections() ([]dto.ConnConfig, error) {
	rows, err := s.db.Query(`SELECT id,name,host,port,user,database_name,params FROM connections ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dto.ConnConfig
	for rows.Next() {
		var c dto.ConnConfig
		if err := rows.Scan(&c.ID, &c.Name, &c.Host, &c.Port, &c.User, &c.Database, &c.Params); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetConnection loads a connection and optionally hydrates password from keyring.
func (s *Store) GetConnection(id string, withPassword bool) (dto.ConnConfig, error) {
	var c dto.ConnConfig
	err := s.db.QueryRow(`SELECT id,name,host,port,user,database_name,params FROM connections WHERE id=?`, id).
		Scan(&c.ID, &c.Name, &c.Host, &c.Port, &c.User, &c.Database, &c.Params)
	if err != nil {
		return c, err
	}
	if withPassword && s.keyring != nil {
		svc, user := s.secretKey(id)
		if pw, err := s.keyring.Get(svc, user); err == nil {
			c.Password = pw
		}
	}
	return c, nil
}

// DeleteConnection removes a connection and its secret.
func (s *Store) DeleteConnection(id string) error {
	if s.keyring != nil {
		svc, user := s.secretKey(id)
		_ = s.keyring.Delete(svc, user)
	}
	_, err := s.db.Exec(`DELETE FROM connections WHERE id=?`, id)
	return err
}

// SaveProfile upserts a sync profile.
func (s *Store) SaveProfile(p dto.SyncProfile) (dto.SyncProfile, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	if p.ID == "" {
		p.ID = uuid.NewString()
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	tables, _ := json.Marshal(p.Tables)
	norm, _ := json.Marshal(p.Normalize)
	allow := 0
	if p.AllowDrop {
		allow = 1
	}
	var exists int
	_ = s.db.QueryRow(`SELECT COUNT(1) FROM profiles WHERE id=?`, p.ID).Scan(&exists)
	if exists == 0 {
		_, err := s.db.Exec(`INSERT INTO profiles(id,name,description,source_id,source_database,target_id,target_database,tables_json,normalize_json,allow_drop,created_at,updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, p.ID, p.Name, p.Description, p.SourceID, p.SourceDatabase, p.TargetID, p.TargetDatabase, string(tables), string(norm), allow, p.CreatedAt, p.UpdatedAt)
		return p, err
	}
	_, err := s.db.Exec(`UPDATE profiles SET name=?,description=?,source_id=?,source_database=?,target_id=?,target_database=?,tables_json=?,normalize_json=?,allow_drop=?,updated_at=? WHERE id=?`,
		p.Name, p.Description, p.SourceID, p.SourceDatabase, p.TargetID, p.TargetDatabase, string(tables), string(norm), allow, p.UpdatedAt, p.ID)
	return p, err
}

// ListProfiles returns all sync profiles.
func (s *Store) ListProfiles() ([]dto.SyncProfile, error) {
	rows, err := s.db.Query(`SELECT id,name,description,source_id,source_database,target_id,target_database,tables_json,normalize_json,allow_drop,created_at,updated_at FROM profiles ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dto.SyncProfile
	for rows.Next() {
		var p dto.SyncProfile
		var tables, norm string
		var allow int
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.SourceID, &p.SourceDatabase, &p.TargetID, &p.TargetDatabase, &tables, &norm, &allow, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(tables), &p.Tables)
		_ = json.Unmarshal([]byte(norm), &p.Normalize)
		p.AllowDrop = allow == 1
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeleteProfile removes a profile.
func (s *Store) DeleteProfile(id string) error {
	_, err := s.db.Exec(`DELETE FROM profiles WHERE id=?`, id)
	return err
}

// ExportProfile returns a profile JSON with no secrets.
func (s *Store) ExportProfile(id string) (string, error) {
	profiles, err := s.ListProfiles()
	if err != nil {
		return "", err
	}
	for _, p := range profiles {
		if p.ID == id {
			b, err := json.MarshalIndent(p, "", "  ")
			return string(b), err
		}
	}
	return "", fmt.Errorf("profile %s not found", id)
}

// AppendAudit writes an audit entry (already redacted DSNs expected).
func (s *Store) AppendAudit(e dto.AuditEntry) error {
	if e.ID == "" {
		e.ID = uuid.NewString()
	}
	ok := 0
	if e.OK {
		ok = 1
	}
	_, err := s.db.Exec(`INSERT INTO audit_log(id,at,action,source_dsn,target_dsn,sql_text,ok,error,duration_ms)
VALUES(?,?,?,?,?,?,?,?,?)`, e.ID, e.At, e.Action, e.SourceDSN, e.TargetDSN, e.SQL, ok, e.Error, e.DurationMs)
	return err
}

// ListAudit returns recent audit entries.
func (s *Store) ListAudit(limit int) ([]dto.AuditEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT id,at,action,source_dsn,target_dsn,sql_text,ok,error,duration_ms FROM audit_log ORDER BY at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dto.AuditEntry
	for rows.Next() {
		var e dto.AuditEntry
		var ok int
		if err := rows.Scan(&e.ID, &e.At, &e.Action, &e.SourceDSN, &e.TargetDSN, &e.SQL, &ok, &e.Error, &e.DurationMs); err != nil {
			return nil, err
		}
		e.OK = ok == 1
		out = append(out, e)
	}
	return out, rows.Err()
}
