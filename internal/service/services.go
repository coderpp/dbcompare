package service

import (
	"context"
	"fmt"
	"time"

	"github.com/pp/db-compare/internal/dto"
	"github.com/pp/db-compare/internal/engine"
	"github.com/pp/db-compare/internal/store"
)

// EventEmitter pushes progress events to the UI shell (Wails / HTTP).
type EventEmitter interface {
	Emit(name string, data any)
}

// NopEmitter discards events.
type NopEmitter struct{}

func (NopEmitter) Emit(string, any) {}

// AppServices groups engine + store for all services.
type AppServices struct {
	Engine *engine.Engine
	Store  *store.Store
	Events EventEmitter
}

func (a *AppServices) emitter() EventEmitter {
	if a.Events == nil {
		return NopEmitter{}
	}
	return a.Events
}

// ConnectionService manages saved MySQL connections.
type ConnectionService struct {
	App *AppServices
}

func (s *ConnectionService) List() ([]dto.ConnConfig, error) {
	return s.App.Store.ListConnections()
}

func (s *ConnectionService) Save(c dto.ConnConfig) (dto.ConnConfig, error) {
	return s.App.Store.SaveConnection(c)
}

func (s *ConnectionService) Delete(id string) error {
	return s.App.Store.DeleteConnection(id)
}

func (s *ConnectionService) hydrateConn(c dto.ConnConfig) (dto.ConnConfig, error) {
	if c.Password == "" && c.ID != "" {
		full, err := s.App.Store.GetConnection(c.ID, true)
		if err != nil {
			return c, err
		}
		if c.Host == "" {
			c = full
		} else {
			c.Password = full.Password
			if c.Name == "" {
				c.Name = full.Name
			}
			if c.Params == "" {
				c.Params = full.Params
			}
		}
	}
	return c, nil
}

func (s *ConnectionService) Test(c dto.ConnConfig) (dto.ServerInfo, error) {
	var err error
	c, err = s.hydrateConn(c)
	if err != nil {
		return dto.ServerInfo{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var conn *engine.Conn
	if c.Database != "" {
		conn, err = engine.Open(ctx, c)
	} else {
		conn, err = engine.OpenServer(ctx, c)
	}
	if err != nil {
		return dto.ServerInfo{}, err
	}
	defer conn.Close()
	return conn.Info, nil
}

func (s *ConnectionService) ListDatabases(c dto.ConnConfig) ([]string, error) {
	var err error
	c, err = s.hydrateConn(c)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return engine.ListDatabases(ctx, c)
}

func (s *ConnectionService) GetWithPassword(id string) (dto.ConnConfig, error) {
	return s.App.Store.GetConnection(id, true)
}

// CompareService runs schema comparisons.
type CompareService struct {
	App *AppServices
}

func (s *CompareService) DefaultNormalize() dto.NormalizeOptions {
	return dto.DefaultNormalizeOptions()
}

func (s *CompareService) TableDiff(sessionID, tableName string) (*dto.TableDiffResult, error) {
	return s.App.Engine.TableDiff(sessionID, tableName)
}

func (s *CompareService) Compare(req dto.CompareRequest) (*dto.CompareResult, error) {
	req, err := s.hydrate(req)
	if err != nil {
		return nil, err
	}
	if req.Source.Database == "" || req.Target.Database == "" {
		return nil, fmt.Errorf("source and target database are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	return s.App.Engine.Compare(ctx, req)
}

func (s *CompareService) hydrate(req dto.CompareRequest) (dto.CompareRequest, error) {
	if req.Source.Password == "" && req.Source.ID != "" {
		c, err := s.App.Store.GetConnection(req.Source.ID, true)
		if err != nil {
			return req, fmt.Errorf("source connection: %w", err)
		}
		req.Source = mergeConn(c, req.Source)
	}
	if req.Target.Password == "" && req.Target.ID != "" {
		c, err := s.App.Store.GetConnection(req.Target.ID, true)
		if err != nil {
			return req, fmt.Errorf("target connection: %w", err)
		}
		req.Target = mergeConn(c, req.Target)
	}
	return req, nil
}

func mergeConn(saved, overlay dto.ConnConfig) dto.ConnConfig {
	if overlay.Host != "" {
		saved.Host = overlay.Host
	}
	if overlay.Port != 0 {
		saved.Port = overlay.Port
	}
	if overlay.User != "" {
		saved.User = overlay.User
	}
	if overlay.Database != "" {
		saved.Database = overlay.Database
	}
	if overlay.Password != "" {
		saved.Password = overlay.Password
	}
	if overlay.Params != "" {
		saved.Params = overlay.Params
	}
	return saved
}

// SyncService plans and applies DDL.
type SyncService struct {
	App *AppServices
}

func (s *SyncService) Plan(req dto.PlanRequest) (*dto.PlanResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return s.App.Engine.Plan(ctx, req)
}

func (s *SyncService) Apply(req dto.ApplyRequest) (*dto.ApplyResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	return s.App.Engine.Apply(ctx, req, func(p dto.ApplyProgress) {
		s.App.emitter().Emit("sync:progress", p)
	}, func(e dto.AuditEntry) {
		_ = s.App.Store.AppendAudit(e)
	})
}

func (s *SyncService) ListAudit(limit int) ([]dto.AuditEntry, error) {
	return s.App.Store.ListAudit(limit)
}

// ProfileService manages saved sync scenarios.
type ProfileService struct {
	App *AppServices
}

func (s *ProfileService) List() ([]dto.SyncProfile, error) {
	return s.App.Store.ListProfiles()
}

func (s *ProfileService) Save(p dto.SyncProfile) (dto.SyncProfile, error) {
	return s.App.Store.SaveProfile(p)
}

func (s *ProfileService) Delete(id string) error {
	return s.App.Store.DeleteProfile(id)
}

func (s *ProfileService) Export(id string) (string, error) {
	return s.App.Store.ExportProfile(id)
}
