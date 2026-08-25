package main

import (
	"embed"
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
	cfgDir := filepath.Join(mustUserConfig(), "dbcompare")
	st, err := store.Open(cfgDir, store.NewSecretStore())
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()

	appSvc := &service.AppServices{
		Engine: engine.New(),
		Store:  st,
	}

	connSvc := &service.ConnectionService{App: appSvc}
	cmpSvc := &service.CompareService{App: appSvc}
	syncSvc := &service.SyncService{App: appSvc}
	profSvc := &service.ProfileService{App: appSvc}

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
