//go:build server

package main

import "github.com/wailsapp/wails/v3/pkg/application"

func macOptions() application.MacOptions {
	// Keep the HTTP server alive when browser tabs disconnect.
	return application.MacOptions{
		ApplicationShouldTerminateAfterLastWindowClosed: false,
	}
}

func shouldCreateWindow() bool { return false }
