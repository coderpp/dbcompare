package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const snapshotAgentLabel = "com.pp.dbcompare.snapshots"

func snapshotAgentPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", snapshotAgentLabel+".plist")
}
func SnapshotBackgroundEnabled(configDir string) bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	if data, err := os.ReadFile(snapshotAgentPath()); err != nil || !strings.Contains(string(data), "<string>"+xmlSnapshotEscape(configDir)+"</string>") {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "launchctl", "print", "gui/"+strconv.Itoa(os.Getuid())+"/"+snapshotAgentLabel).Run() == nil
}
func configureSnapshotBackground(enabled bool, configDir string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("后台调度目前仅支持 macOS")
	}
	path := snapshotAgentPath()
	if data, err := os.ReadFile(path); err == nil && !strings.Contains(string(data), "<string>"+xmlSnapshotEscape(configDir)+"</string>") {
		return fmt.Errorf("后台调度已绑定其他配置目录，请在原配置中关闭后再启用")
	}
	domain := "gui/" + strconv.Itoa(os.Getuid())
	run := func(args ...string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := exec.CommandContext(ctx, "launchctl", args...).Run(); err != nil {
			return fmt.Errorf("macOS 后台调度操作失败: %w", err)
		}
		return nil
	}
	if !enabled {
		if SnapshotBackgroundEnabled(configDir) {
			if err := run("bootout", domain+"/"+snapshotAgentLabel); err != nil {
				return err
			}
		}
		err := os.Remove(path)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	binary := "/Applications/DB Compare.app/Contents/MacOS/dbcompare"
	if _, err := os.Stat(binary); err != nil {
		return fmt.Errorf("请先将新版 DB Compare 安装到 /Applications")
	}
	if SnapshotBackgroundEnabled(configDir) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	// User-selected background mode uses the normal user config/keychain; never embeds secrets.

	data := snapshotAgentXML(snapshotAgentLabel, binary, configDir)
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		return err
	}
	if err := run("bootstrap", domain, path); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

func xmlSnapshotEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;").Replace(s)
}

func snapshotAgentXML(label, binary, configDir string) string {
	return `<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>Label</key><string>` + label + `</string><key>ProgramArguments</key><array><string>` + xmlSnapshotEscape(binary) + `</string><string>--snapshot-worker</string></array><key>EnvironmentVariables</key><dict><key>DBCOMPARE_CONFIG_DIR</key><string>` + xmlSnapshotEscape(configDir) + `</string></dict><key>StartInterval</key><integer>60</integer><key>RunAtLoad</key><true/><key>ProcessType</key><string>Background</string></dict></plist>`
}
