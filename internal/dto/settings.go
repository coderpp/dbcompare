package dto

// Password is populated only by an explicitly requested credential export.
type SettingsConnection struct {
	Password string `json:"password,omitempty"`
	ID       string `json:"id"`
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Database string `json:"database,omitempty"`
	Params   string `json:"params,omitempty"`
}

type SettingsBundle struct {
	IncludesPasswords bool                 `json:"includesPasswords,omitempty"`
	Format            string               `json:"format"`
	Version           int                  `json:"version"`
	ExportedAt        string               `json:"exportedAt"`
	Connections       []SettingsConnection `json:"connections"`
	Profiles          []SyncProfile        `json:"profiles"`
}

type SettingsExportRequest struct {
	IncludePasswords bool     `json:"includePasswords"`
	All              bool     `json:"all"`
	ConnectionIDs    []string `json:"connectionIds"`
	ProfileIDs       []string `json:"profileIds"`
}

type SettingsExportFile struct {
	Filename string   `json:"filename"`
	Content  string   `json:"content"`
	Warnings []string `json:"warnings"`
	Path     string   `json:"path,omitempty"`
}

type SettingsPreview struct {
	PasswordConnectionIDs  []string       `json:"passwordConnectionIds"`
	Bundle                 SettingsBundle `json:"bundle"`
	Warnings               []string       `json:"warnings"`
	ConflictingConnections []string       `json:"conflictingConnections"`
	ConflictingProfiles    []string       `json:"conflictingProfiles"`
}

type SettingsImportResult struct {
	PasswordsImported int                  `json:"passwordsImported"`
	Connections       []SettingsConnection `json:"connections"`
	Profiles          []SyncProfile        `json:"profiles"`
	Warnings          []string             `json:"warnings"`
}
