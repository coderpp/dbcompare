package main

import (
	"context"
	"embed"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/pp/db-compare/internal/engine"
	"github.com/pp/db-compare/internal/service"
	"github.com/pp/db-compare/internal/store"
)

//go:embed all:frontend/dist
var assets embed.FS

type wailsEmitter struct {
	app *application.App
}

func (e wailsEmitter) Emit(name string, data any) {
	if e.app != nil {
		e.app.Event.Emit(name, data)
	}
}

func main() {
	cfgDir := os.Getenv("DBCOMPARE_CONFIG_DIR")
	if cfgDir == "" {
		cfgDir = filepath.Join(mustUserConfig(), "dbcompare")
	}
	st, err := store.Open(cfgDir, store.NewSecretStore())
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()

	appSvc := &service.AppServices{
		Engine: engine.New(),
		Store:  st,
	}

	snapshotSvc := &service.SnapshotService{App: appSvc}
	if len(os.Args) > 1 && os.Args[1] == "--snapshot-worker" {
		if err := service.RunSnapshotWorker(snapshotSvc); err != nil {
			log.Print("Snapshot worker could not complete; see snapshot task history")
			_ = st.Close()
			os.Exit(1)
		}
		return
	}
	schedulerCtx, stopScheduler := context.WithCancel(context.Background())
	defer stopScheduler()
	connSvc := &service.ConnectionService{App: appSvc}
	cmpSvc := &service.CompareService{App: appSvc}
	syncSvc := &service.SyncService{App: appSvc}
	profSvc := &service.ProfileService{App: appSvc}
	settingsSvc := &service.SettingsService{App: appSvc}

	port := 13880
	if v := os.Getenv("DBCOMPARE_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			port = n
		}
	}

	app := application.New(application.Options{
		Name:        "DB Compare",
		Description: "MySQL schema compare & sync (5.7 ↔ 8.0)",
		Services: []application.Service{
			application.NewService(connSvc),
			application.NewService(cmpSvc),
			application.NewService(syncSvc),
			application.NewService(profSvc),
			application.NewService(settingsSvc),
			application.NewService(snapshotSvc),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Server: application.ServerOptions{
			Host: "127.0.0.1",
			Port: port,
		},
		Mac: macOptions(),
	})
	appSvc.Events = wailsEmitter{app: app}
	service.StartSnapshotScheduler(schedulerCtx, snapshotSvc)
	if shouldCreateWindow() {
		appSvc.OpenArchive = func() (string, error) {
			path, err := app.Dialog.OpenFile().SetTitle("导入结构快照包").AddFilter("JSON 快照包", "*.json").PromptForSingleSelection()
			if err != nil || path == "" {
				return "", err
			}
			file, err := os.Open(path)
			if err != nil {
				return "", err
			}
			defer file.Close()
			raw, err := io.ReadAll(io.LimitReader(file, (64<<20)+1))
			if err != nil {
				return "", err
			}
			if len(raw) > 64<<20 {
				return "", fmt.Errorf("快照包不能超过 64 MB")
			}
			return string(raw), nil
		}
		appSvc.OpenFile = func() (string, error) {
			path, err := app.Dialog.OpenFile().SetTitle("导入连接与方案").AddFilter("JSON 配置包", "*.json").PromptForSingleSelection()
			if err != nil || path == "" {
				return "", err
			}
			file, err := os.Open(path)
			if err != nil {
				return "", err
			}
			defer file.Close()
			content, err := io.ReadAll(io.LimitReader(file, service.MaxSettingsBytes+1))
			if err != nil {
				return "", err
			}
			if len(content) == 0 {
				return "", fmt.Errorf("配置文件为空")
			}
			if len(content) > service.MaxSettingsBytes {
				return "", fmt.Errorf("配置文件不能超过 8 MB")
			}
			return string(content), nil
		}
		appSvc.SaveFile = func(filename, content string) (string, error) {
			path, err := app.Dialog.SaveFile().SetFilename(filename).CanCreateDirectories(true).PromptForSingleSelection()
			if err != nil || path == "" {
				return path, err
			}
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				return "", err
			}
			return path, nil
		}

	}
	if shouldCreateWindow() {
		app.Window.NewWithOptions(application.WebviewWindowOptions{
			Title:            "DB Compare",
			Width:            1280,
			Height:           800,
			MinWidth:         960,
			MinHeight:        600,
			BackgroundColour: application.NewRGB(244, 246, 248),
			URL:              "/",
			// Use a normal title bar so the window is easy to find on macOS.
			Mac: application.MacWindow{
				Backdrop: application.MacBackdropNormal,
				TitleBar: application.MacTitleBarDefault,
			},
		})
	}

	log.Printf("DB Compare starting (server port %d when built with -tags server)", port)
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

func mustUserConfig() string {
	d, err := os.UserConfigDir()
	if err != nil {
		d, _ = os.UserHomeDir()
	}
	return d
}
