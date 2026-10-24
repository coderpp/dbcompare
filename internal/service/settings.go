package service

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/pp/db-compare/internal/dto"
)

const MaxSettingsBytes = 8 << 20
const settingsFormat = "dbcompare-settings"

type SettingsService struct{ App *AppServices }

// Only driver options with non-secret, portable values cross machines. Arbitrary
// session variables, local certificate/key paths and unknown options are omitted.
func portableParams(raw string) (string, bool) {
	if raw == "" {
		return "", false
	}
	values, err := url.ParseQuery(strings.TrimPrefix(raw, "?"))
	if err != nil {
		return "", true
	}
	allowed := map[string]bool{}
	for _, key := range []string{"charset", "collation", "timeout", "readTimeout", "writeTimeout", "parseTime", "loc", "interpolateParams", "multiStatements", "clientFoundRows", "columnsWithAlias", "rejectReadOnly", "allowNativePasswords", "allowCleartextPasswords", "allowOldPasswords", "checkConnLiveness", "maxAllowedPacket"} {
		allowed[key] = true
	}
	out := url.Values{}
	omitted := false
	for key, value := range values {
		if allowed[key] {
			out[key] = value
		} else if key == "tls" && len(value) == 1 && (value[0] == "true" || value[0] == "false" || value[0] == "preferred" || value[0] == "skip-verify") {
			out[key] = value
		} else {
			omitted = true
		}
	}
	return out.Encode(), omitted
}

func (s *SettingsService) Export(req dto.SettingsExportRequest) (*dto.SettingsExportFile, error) {
	connections, err := s.App.Store.ListConnections()
	if err != nil {
		return nil, err
	}
	profiles, err := s.App.Store.ListProfiles()
	if err != nil {
		return nil, err
	}
	wantC, wantP := map[string]bool{}, map[string]bool{}
	for _, id := range req.ConnectionIDs {
		wantC[id] = true
	}
	for _, id := range req.ProfileIDs {
		wantP[id] = true
	}
	if !req.All && len(wantC)+len(wantP) == 0 {
		return nil, fmt.Errorf("请选择要导出的连接或方案")
	}
	bundle := dto.SettingsBundle{Format: settingsFormat, Version: 1, ExportedAt: time.Now().UTC().Format(time.RFC3339), Connections: []dto.SettingsConnection{}, Profiles: []dto.SyncProfile{}}
	if req.IncludePasswords {
		bundle.Version = 2
		bundle.IncludesPasswords = true
	}
	for _, p := range profiles {
		if req.All || wantP[p.ID] {
			bundle.Profiles = append(bundle.Profiles, p)
			wantC[p.SourceID] = true
			wantC[p.TargetID] = true
			delete(wantP, p.ID)
		}
	}
	if len(wantP) > 0 {
		return nil, fmt.Errorf("要导出的方案已不存在，请刷新列表")
	}
	warnings := []string{}
	for _, c := range connections {
		if !req.All && !wantC[c.ID] {
			continue
		}
		params, omitted := portableParams(c.Params)
		if omitted {
			warnings = append(warnings, fmt.Sprintf("连接「%s」的自定义、敏感或本机专用参数未导出，请在目标机补充", c.Name))
		}
		transfer := dto.SettingsConnection{ID: c.ID, Name: c.Name, Host: c.Host, Port: c.Port, User: c.User, Database: c.Database, Params: params}
		if req.IncludePasswords {
			transfer.Password, err = s.App.Store.PasswordForExport(c.ID)
			if err != nil {
				return nil, fmt.Errorf("无法读取连接「%s」的密码，请检查系统凭据存储权限", c.Name)
			}
			if transfer.Password == "" {
				warnings = append(warnings, fmt.Sprintf("连接「%s」没有可导出的已保存密码；目标机可能需要补充", c.Name))
			}
		}
		bundle.Connections = append(bundle.Connections, transfer)
		delete(wantC, c.ID)
	}
	if len(wantC) > 0 {
		return nil, fmt.Errorf("连接已不存在，或方案引用了已删除的连接；请修复后重新导出")
	}
	if len(bundle.Connections)+len(bundle.Profiles) == 0 {
		return nil, fmt.Errorf("没有可导出的配置")
	}
	b, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return nil, err
	}
	suffix := ".json"
	if req.IncludePasswords {
		suffix = "-with-passwords.json"
	}
	return &dto.SettingsExportFile{Filename: "dbcompare-settings-" + time.Now().Format("20060102-150405") + suffix, Content: string(b), Warnings: warnings}, nil
}

func (s *SettingsService) SaveExport(req dto.SettingsExportRequest) (*dto.SettingsExportFile, error) {
	if s.App.SaveFile == nil {
		return nil, fmt.Errorf("当前运行环境不支持保存对话框，请使用文件下载")
	}
	file, err := s.Export(req)
	if err != nil {
		return nil, err
	}
	file.Path, err = s.App.SaveFile(file.Filename, file.Content)
	// Only the save result is needed in the desktop client.
	file.Content = ""
	return file, err
}

func (s *SettingsService) ReadImportFile() (string, error) {
	if s.App.OpenFile == nil {
		return "", fmt.Errorf("当前运行环境不支持打开对话框，请选择文件上传")
	}
	return s.App.OpenFile()
}

func parseSettings(content string) (*dto.SettingsBundle, []string, error) {
	if len(content) > MaxSettingsBytes {
		return nil, nil, fmt.Errorf("配置文件不能超过 8 MB")
	}
	content = strings.TrimPrefix(content, "\ufeff")
	var b dto.SettingsBundle
	if err := json.Unmarshal([]byte(content), &b); err != nil {
		return nil, nil, fmt.Errorf("无法读取 JSON 配置文件，请检查格式")
	}
	if b.Format != settingsFormat || (b.Version != 1 && b.Version != 2) {
		return nil, nil, fmt.Errorf("不支持此配置文件格式或版本，请使用“连接与方案”中的导出功能生成配置包")
	}
	if b.Version == 2 && !b.IncludesPasswords {
		return nil, nil, fmt.Errorf("含密码配置包缺少 includesPasswords 标记")
	}
	if b.Connections == nil {
		b.Connections = []dto.SettingsConnection{}
	}
	if b.Profiles == nil {
		b.Profiles = []dto.SyncProfile{}
	}
	if len(b.Connections)+len(b.Profiles) == 0 {
		return nil, nil, fmt.Errorf("配置包中没有连接或方案")
	}
	if len(b.Connections) > 2000 || len(b.Profiles) > 2000 {
		return nil, nil, fmt.Errorf("单次最多导入 2000 个连接和 2000 个方案")
	}
	ids := map[string]bool{}
	pids := map[string]bool{}
	warnings := []string{}
	for i := range b.Connections {
		c := &b.Connections[i]
		if b.Version == 1 {
			c.Password = ""
		} // Preserve v1 no-credentials import behavior.
		c.Name = strings.TrimSpace(c.Name)
		c.Host = strings.TrimSpace(c.Host)
		c.User = strings.TrimSpace(c.User)
		if c.ID == "" || ids[c.ID] {
			return nil, nil, fmt.Errorf("连接标识为空或重复")
		}
		ids[c.ID] = true
		if c.Name == "" || c.Host == "" || c.User == "" || c.Port < 1 || c.Port > 65535 {
			return nil, nil, fmt.Errorf("连接「%s」的名称、主机、端口或用户无效", c.Name)
		}
		params, omitted := portableParams(c.Params)
		c.Params = params
		if omitted {
			warnings = append(warnings, fmt.Sprintf("连接「%s」的自定义、敏感或本机专用参数将被忽略", c.Name))
		}
	}
	for _, p := range b.Profiles {
		if p.ID == "" || pids[p.ID] || strings.TrimSpace(p.Name) == "" {
			return nil, nil, fmt.Errorf("方案名称或标识为空，或标识重复")
		}
		pids[p.ID] = true
		if !ids[p.SourceID] || !ids[p.TargetID] {
			return nil, nil, fmt.Errorf("方案「%s」引用的连接不在配置包中，请连同关联连接一起导出", p.Name)
		}
		if p.SourceDatabase == "" || p.TargetDatabase == "" {
			return nil, nil, fmt.Errorf("方案「%s」缺少源库或目标库", p.Name)
		}
	}
	return &b, warnings, nil
}

func (s *SettingsService) PreviewImport(content string) (*dto.SettingsPreview, error) {
	b, warnings, err := parseSettings(content)
	if err != nil {
		return nil, err
	}
	cs, err := s.App.Store.ListConnections()
	if err != nil {
		return nil, err
	}
	ps, err := s.App.Store.ListProfiles()
	if err != nil {
		return nil, err
	}
	cn, pn := map[string]bool{}, map[string]bool{}
	for _, c := range cs {
		cn[strings.ToLower(c.Name)] = true
	}
	for _, p := range ps {
		pn[strings.ToLower(p.Name)] = true
	}
	passwordIDs := []string{}
	for i := range b.Connections {
		if b.Connections[i].Password != "" {
			passwordIDs = append(passwordIDs, b.Connections[i].ID)
		}
		b.Connections[i].Password = "" // Preview never echoes credentials.
	}
	out := &dto.SettingsPreview{PasswordConnectionIDs: passwordIDs, Bundle: *b, Warnings: warnings, ConflictingConnections: []string{}, ConflictingProfiles: []string{}}
	for _, c := range b.Connections {
		key := strings.ToLower(c.Name)
		if cn[key] {
			out.ConflictingConnections = append(out.ConflictingConnections, c.ID)
		}
		cn[key] = true
	}
	for _, p := range b.Profiles {
		key := strings.ToLower(strings.TrimSpace(p.Name))
		if pn[key] {
			out.ConflictingProfiles = append(out.ConflictingProfiles, p.ID)
		}
		pn[key] = true
	}
	return out, nil
}

func (s *SettingsService) Import(content string) (*dto.SettingsImportResult, error) {
	b, warnings, err := parseSettings(content)
	if err != nil {
		return nil, err
	}
	out, err := s.App.Store.ImportSettings(*b)
	if err != nil {
		return nil, err
	}
	out.Warnings = append(out.Warnings, warnings...)
	return out, nil
}
