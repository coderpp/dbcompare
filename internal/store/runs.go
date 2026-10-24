package store

import (
	"encoding/json"
	"fmt"
	"github.com/pp/db-compare/internal/dto"
)

func (s *Store) SaveRun(run *dto.SyncRun) error {
	cp := *run
	cp.Source.Password = ""
	cp.Source.Params = ""
	cp.Target.Password = ""
	cp.Target.Params = ""
	data, err := json.Marshal(&cp)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO sync_runs(id,started_at,payload) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload`, cp.ID, cp.StartedAt, string(data))
	return err
}
func (s *Store) GetRun(id string) (*dto.SyncRun, error) {
	var data string
	if err := s.db.QueryRow(`SELECT payload FROM sync_runs WHERE id=?`, id).Scan(&data); err != nil {
		return nil, err
	}
	var run dto.SyncRun
	if err := json.Unmarshal([]byte(data), &run); err != nil {
		return nil, fmt.Errorf("read sync report: %w", err)
	}
	return &run, nil
}
func (s *Store) ListRuns(limit int) ([]dto.SyncRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT payload FROM sync_runs ORDER BY started_at DESC, rowid DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := make([]dto.SyncRun, 0)
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var run dto.SyncRun
		if err := json.Unmarshal([]byte(data), &run); err != nil {
			return nil, err
		}
		run.Snapshots = nil
		run.Outcomes = nil
		run.Remaining = nil
		run.Repairs = nil
		runs = append(runs, run)
	}
	return runs, rows.Err()
}
