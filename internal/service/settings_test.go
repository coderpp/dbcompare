package service

import (
	"encoding/json"
	"errors"
	"github.com/pp/db-compare/internal/dto"
	"github.com/pp/db-compare/internal/store"
	"strings"
	"testing"
)

func settingsTest(t *testing.T) (*SettingsService, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir(), store.NewMemorySecrets())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &SettingsService{App: &AppServices{Store: st}}, st
}
func seedSettings(t *testing.T, st *store.Store) (dto.ConnConfig, dto.SyncProfile) {
	t.Helper()
	c, err := st.SaveConnection(dto.ConnConfig{Name: "本地", Host: "127.0.0.1", Port: 3306, User: "root", Password: "never-export-this", Params: "charset=utf8mb4&timeout=5s&password=hidden-param&access_token=hidden-token&custom=hidden-custom&tls=true"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.SaveProfile(dto.SyncProfile{Name: "发布方案", SourceID: c.ID, TargetID: c.ID, SourceDatabase: "源库", TargetDatabase: "目标库", Normalize: dto.DefaultNormalizeOptions(), AllowDrop: true, Tables: []string{"a", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	return c, p
}
func TestSettingsPortableRoundTrip(t *testing.T) {
	svc, src := settingsTest(t)
	c, p := seedSettings(t, src)
	file, err := svc.Export(dto.SettingsExportRequest{ProfileIDs: []string{p.ID}})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"never-export-this", "hidden-param", "hidden-token", "hidden-custom", "\"password\""} {
		if strings.Contains(file.Content, secret) {
			t.Fatalf("export leaked %s", secret)
		}
	}
	if len(file.Warnings) != 1 {
		t.Fatal("missing params omission warning")
	}
	var bundle dto.SettingsBundle
	if err = json.Unmarshal([]byte(file.Content), &bundle); err != nil {
		t.Fatal(err)
	}
	if len(bundle.Connections) != 1 || len(bundle.Profiles) != 1 || !strings.Contains(bundle.Connections[0].Params, "charset=utf8mb4") {
		t.Fatal("export dependency or params lost")
	}
	target, dst := settingsTest(t)
	existing, _ := dst.SaveConnection(dto.ConnConfig{ID: c.ID, Name: c.Name, Host: "existing", Port: 3306, User: "keep", Password: "keep-existing-secret"})
	preview, err := target.PreviewImport(file.Content)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.ConflictingConnections) != 1 {
		t.Fatal("name conflict not previewed")
	}
	cs, _ := dst.ListConnections()
	if len(cs) != 1 {
		t.Fatal("preview wrote data")
	}
	result, err := target.Import(file.Content)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Connections) != 1 || len(result.Profiles) != 1 {
		t.Fatal("incomplete import")
	}
	imported := result.Connections[0]
	profile := result.Profiles[0]
	if imported.ID == c.ID || imported.Name == c.Name || profile.ID == p.ID {
		t.Fatal("import overwrote or reused foreign IDs")
	}
	if profile.SourceID != imported.ID || profile.TargetID != imported.ID || profile.SourceDatabase != p.SourceDatabase || profile.TargetDatabase != p.TargetDatabase || !profile.Normalize.StripIntDisplayWidth || !profile.AllowDrop || len(profile.Tables) != 2 {
		t.Fatal("profile mapping/options lost")
	}
	old, _ := dst.GetConnection(existing.ID, true)
	if old.Host != "existing" || old.Password != "keep-existing-secret" {
		t.Fatal("existing connection or keychain changed")
	}
	full, _ := dst.GetConnection(imported.ID, true)
	if full.Password != "" {
		t.Fatal("imported credentials")
	}
	second, err := target.Import(file.Content)
	if err != nil {
		t.Fatal(err)
	}
	if second.Connections[0].ID == imported.ID || second.Connections[0].Name == imported.Name || second.Profiles[0].Name == profile.Name {
		t.Fatal("repeat import collision")
	}
}
func TestSettingsRejectBeforeWriting(t *testing.T) {
	svc, st := settingsTest(t)
	_, p := seedSettings(t, st)
	file, _ := svc.Export(dto.SettingsExportRequest{ProfileIDs: []string{p.ID}})
	var valid dto.SettingsBundle
	json.Unmarshal([]byte(file.Content), &valid)
	cases := map[string]func(*dto.SettingsBundle){"version": func(b *dto.SettingsBundle) { b.Version = 99 }, "missing connection": func(b *dto.SettingsBundle) { b.Profiles[0].SourceID = "missing" }, "duplicate id": func(b *dto.SettingsBundle) { b.Connections = append(b.Connections, b.Connections[0]) }, "bad port": func(b *dto.SettingsBundle) { b.Connections[0].Port = 70000 }}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			var b dto.SettingsBundle
			json.Unmarshal([]byte(file.Content), &b)
			mutate(&b)
			raw, _ := json.Marshal(b)
			if _, err := svc.Import(string(raw)); err == nil {
				t.Fatal("accepted invalid bundle")
			}
			cs, _ := st.ListConnections()
			ps, _ := st.ListProfiles()
			if len(cs) != 1 || len(ps) != 1 {
				t.Fatal("partial import")
			}
		})
	}
	if _, err := svc.PreviewImport("not json"); err == nil {
		t.Fatal("accepted invalid json")
	}
	if _, err := svc.Import(strings.Repeat("x", MaxSettingsBytes+1)); err == nil {
		t.Fatal("accepted oversized file")
	}
	// Failed late in the transaction: the inserted connection must roll back too.
	valid.Profiles[0].TargetID = "missing"
	if _, err := st.ImportSettings(valid); err == nil {
		t.Fatal("expected missing dependency")
	}
	cs, _ := st.ListConnections()
	if len(cs) != 1 {
		t.Fatal("transaction left partial connections")
	}
}
func TestSettingsExportScopesAndSaveCancel(t *testing.T) {
	svc, st := settingsTest(t)
	c, p := seedSettings(t, st)
	file, err := svc.Export(dto.SettingsExportRequest{ConnectionIDs: []string{c.ID}})
	if err != nil {
		t.Fatal(err)
	}
	var b dto.SettingsBundle
	json.Unmarshal([]byte(file.Content), &b)
	if len(b.Connections) != 1 || len(b.Profiles) != 0 {
		t.Fatal("connection-only scope wrong")
	}
	for _, req := range []dto.SettingsExportRequest{{}, {ProfileIDs: []string{"missing"}}, {ConnectionIDs: []string{"missing"}}} {
		if _, err := svc.Export(req); err == nil {
			t.Fatal("accepted missing selection")
		}
	}
	svc.App.SaveFile = func(name, content string) (string, error) {
		if !strings.HasSuffix(name, ".json") || !strings.Contains(content, "dbcompare-settings") {
			t.Fatal("incorrect saved artifact")
		}
		return "", nil
	}
	saved, err := svc.SaveExport(dto.SettingsExportRequest{All: true})
	if err != nil || saved.Path != "" || saved.Content != "" {
		t.Fatal("cancel handling failed")
	}
	st.DeleteConnection(c.ID)
	if _, err := svc.Export(dto.SettingsExportRequest{ProfileIDs: []string{p.ID}}); err == nil {
		t.Fatal("exported orphan profile")
	}
}

func TestSettingsConnectionOnlyNullProfilesAndIgnoredPasswords(t *testing.T) {
	svc, st := settingsTest(t)
	raw := `{"format":"dbcompare-settings","version":1,"connections":[{"id":"foreign","name":"示例","host":"localhost","port":3306,"user":"demo","password":"must-not-import"}],"profiles":null}`
	preview, err := svc.PreviewImport(raw)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Bundle.Profiles == nil {
		t.Fatal("preview arrays must not be null")
	}
	result, err := svc.Import(raw)
	if err != nil {
		t.Fatal(err)
	}
	full, err := st.GetConnection(result.Connections[0].ID, true)
	if err != nil || full.Password != "" {
		t.Fatal("file credential was imported")
	}
}

func TestSettingsOptionalPasswordsRoundTrip(t *testing.T) {
	svc, st := settingsTest(t)
	_, profile := seedSettings(t, st)
	file, err := svc.Export(dto.SettingsExportRequest{ProfileIDs: []string{profile.ID}, IncludePasswords: true})
	if err != nil {
		t.Fatal(err)
	}
	var bundle dto.SettingsBundle
	json.Unmarshal([]byte(file.Content), &bundle)
	if bundle.Version != 2 || !bundle.IncludesPasswords || bundle.Connections[0].Password != "never-export-this" || !strings.HasSuffix(file.Filename, "-with-passwords.json") {
		t.Fatal("explicit credential export incomplete")
	}
	for _, value := range []string{"hidden-param", "hidden-token", "hidden-custom"} {
		if strings.Contains(file.Content, value) {
			t.Fatal("DSN secrets must still be omitted")
		}
	}
	target, dst := settingsTest(t)
	preview, err := target.PreviewImport(file.Content)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(preview)
	if strings.Contains(string(b), "never-export-this") || len(preview.PasswordConnectionIDs) != 1 {
		t.Fatal("preview revealed credential or lost metadata")
	}
	result, err := target.Import(file.Content)
	if err != nil {
		t.Fatal(err)
	}
	full, _ := dst.GetConnection(result.Connections[0].ID, true)
	if full.Password != "never-export-this" || result.PasswordsImported != 1 {
		t.Fatal("import did not save credential")
	}
	b, _ = json.Marshal(result)
	if strings.Contains(string(b), "never-export-this") {
		t.Fatal("import response leaked credential")
	}
	plain, err := target.Export(dto.SettingsExportRequest{All: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain.Content, "never-export-this") || strings.Contains(plain.Content, "\"password\"") {
		t.Fatal("later default export leaked credential")
	}
	svc.App.SaveFile = func(name, content string) (string, error) {
		if !strings.Contains(content, "never-export-this") {
			t.Fatal("native export omitted opted-in credential")
		}
		return "/synthetic/export.json", nil
	}
	saved, err := svc.SaveExport(dto.SettingsExportRequest{All: true, IncludePasswords: true})
	if err != nil || saved.Content != "" {
		t.Fatal("native save echoed credential")
	}
	bundle.IncludesPasswords = false
	raw, _ := json.Marshal(bundle)
	if _, err := target.Import(string(raw)); err == nil {
		t.Fatal("accepted unmarked v2 credential file")
	}
}

type deniedExportSecrets struct{ reads int }

func (s *deniedExportSecrets) Set(string, string, string) error { return nil }
func (s *deniedExportSecrets) Get(string, string) (string, error) {
	s.reads++
	return "", errors.New("synthetic access denial")
}
func (s *deniedExportSecrets) Delete(string, string) error { return nil }
func TestDefaultExportNeverReadsCredentialStore(t *testing.T) {
	secrets := &deniedExportSecrets{}
	st, err := store.Open(t.TempDir(), secrets)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_, err = st.SaveConnection(dto.ConnConfig{Name: "example", Host: "localhost", Port: 3306, User: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	svc := &SettingsService{App: &AppServices{Store: st}}
	if _, err := svc.Export(dto.SettingsExportRequest{All: true}); err != nil || secrets.reads != 0 {
		t.Fatal("default export accessed credentials")
	}
	if _, err := svc.Export(dto.SettingsExportRequest{All: true, IncludePasswords: true}); err == nil || secrets.reads != 1 {
		t.Fatal("explicit export hid credential access failure")
	}
	if _, err := svc.SaveExport(dto.SettingsExportRequest{All: true, IncludePasswords: true}); err == nil || secrets.reads != 1 {
		t.Fatal("unsupported dialog read credentials unnecessarily")
	}
}
