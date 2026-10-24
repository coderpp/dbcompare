package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pp/db-compare/internal/dto"
)

// ImportSettings creates new records atomically. Imported IDs are never trusted
// as local IDs. Credentials use new keychain IDs and are cleaned on failure.
func (s *Store) ImportSettings(bundle dto.SettingsBundle) (result *dto.SettingsImportResult, resultErr error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	committed := false
	secretIDs := []string{}
	defer func() {
		if committed {
			return
		}
		_ = tx.Rollback()
		for _, id := range secretIDs {
			svc, user := s.secretKey(id)
			if err := s.keyring.Delete(svc, user); err != nil && !isMissingSecret(err) {
				resultErr = fmt.Errorf("%w；部分新凭据清理失败", resultErr)
			}
		}
	}()
	getNames := func(table string) (map[string]bool, error) {
		rows, err := tx.Query("SELECT name FROM " + table)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		names := map[string]bool{}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return nil, err
			}
			names[strings.ToLower(name)] = true
		}
		return names, rows.Err()
	}
	connections, err := getNames("connections")
	if err != nil {
		return nil, err
	}
	profiles, err := getNames("profiles")
	if err != nil {
		return nil, err
	}
	uniqueName := func(name string, used map[string]bool) string {
		base := strings.TrimSpace(name)
		result := base
		for i := 1; used[strings.ToLower(result)]; i++ {
			result = fmt.Sprintf("%s（导入 %d）", base, i)
		}
		used[strings.ToLower(result)] = true
		return result
	}
	now := time.Now().UTC().Format(time.RFC3339)
	ids := map[string]string{}
	out := &dto.SettingsImportResult{Connections: []dto.SettingsConnection{}, Profiles: []dto.SyncProfile{}, Warnings: []string{}}
	passwords := map[string]string{}
	for _, c := range bundle.Connections {
		oldID := c.ID
		if oldID == "" || ids[oldID] != "" {
			return nil, fmt.Errorf("连接标识为空或重复")
		}
		c.ID = uuid.NewString()
		ids[oldID] = c.ID
		c.Name = uniqueName(c.Name, connections)
		_, err = tx.Exec(`INSERT INTO connections(id,name,host,port,user,database_name,params,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`, c.ID, c.Name, c.Host, c.Port, c.User, c.Database, c.Params, now, now)
		if err != nil {
			return nil, err
		}
		if c.Password != "" {
			passwords[c.ID] = c.Password
		}
		c.Password = ""
		out.Connections = append(out.Connections, c)
	}
	for _, p := range bundle.Profiles {
		if ids[p.SourceID] == "" || ids[p.TargetID] == "" {
			return nil, fmt.Errorf("方案「%s」缺少关联连接", p.Name)
		}
		p.ID = uuid.NewString()
		p.Name = uniqueName(p.Name, profiles)
		p.SourceID = ids[p.SourceID]
		p.TargetID = ids[p.TargetID]
		p.CreatedAt = now
		p.UpdatedAt = now
		tables, err := json.Marshal(p.Tables)
		if err != nil {
			return nil, err
		}
		norm, err := json.Marshal(p.Normalize)
		if err != nil {
			return nil, err
		}
		allow := 0
		if p.AllowDrop {
			allow = 1
		}
		_, err = tx.Exec(`INSERT INTO profiles(id,name,description,source_id,source_database,target_id,target_database,tables_json,normalize_json,allow_drop,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, p.ID, p.Name, p.Description, p.SourceID, p.SourceDatabase, p.TargetID, p.TargetDatabase, string(tables), string(norm), allow, now, now)
		if err != nil {
			return nil, err
		}
		out.Profiles = append(out.Profiles, p)
	}
	// All SQL and reference validation succeeds before touching the keychain.
	for _, c := range out.Connections {
		password := passwords[c.ID]
		if password == "" {
			continue
		}
		if s.keyring == nil {
			return nil, fmt.Errorf("凭据存储不可用，导入已取消")
		}
		svc, user := s.secretKey(c.ID)
		secretIDs = append(secretIDs, c.ID)
		if err := s.keyring.Set(svc, user, password); err != nil {
			return nil, fmt.Errorf("保存连接「%s」密码失败，导入已取消", c.Name)
		}
		out.PasswordsImported++
		if temporary, ok := s.keyring.(interface{ IsTemporary(string, string) bool }); ok && temporary.IsTemporary(svc, user) {
			out.Warnings = append(out.Warnings, fmt.Sprintf("连接「%s」的密码仅保存在内存中，重启后需重新填写；请检查系统凭据存储", c.Name))
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	committed = true
	return out, nil
}
