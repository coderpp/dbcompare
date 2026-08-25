//go:build !server

package main

import "github.com/wailsapp/wails/v3/pkg/application"

func macOptions() application.MacOptions {
	return application.MacOptions{
		ApplicationShouldTerminateAfterLastWindowClosed: true,
	}
}

func shouldCreateWindow() bool { return true }
