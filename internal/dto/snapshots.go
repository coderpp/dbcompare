package dto

// SnapshotTarget stores references only. Secrets are resolved at capture time.
type SnapshotTarget struct {
	ConnectionID string `json:"connectionId"`
	Database     string `json:"database"`
}
type SnapshotTask struct {
	ID            string           `json:"id"`
	Name          string           `json:"name"`
	Targets       []SnapshotTarget `json:"targets"`
	Schedule      string           `json:"schedule"` // interval, daily, weekly
	IntervalHours int              `json:"intervalHours"`
	Time          string           `json:"time"`
	Weekdays      []int            `json:"weekdays"` // Sunday=0
	RetentionDays int              `json:"retentionDays"`
	Enabled       bool             `json:"enabled"`
	Background    bool             `json:"background"`
	NextRun       string           `json:"nextRun"`
}
type SchemaArchive struct {
	Format   int             `json:"format"`
	Database string          `json:"database"`
	Server   ServerInfo      `json:"server"`
	Model    string          `json:"model"`
	Tables   []TableSnapshot `json:"tables"`
	Warnings []string        `json:"warnings"`
}
type SnapshotRecord struct {
	ID             string   `json:"id"`
	TaskID         string   `json:"taskId"`
	ConnectionID   string   `json:"connectionId"`
	ConnectionName string   `json:"connectionName"`
	Database       string   `json:"database"`
	CapturedAt     string   `json:"capturedAt"`
	LastCheckedAt  string   `json:"lastCheckedAt,omitempty"`
	Hash           string   `json:"hash"`
	TableCount     int      `json:"tableCount"`
	Unchanged      bool     `json:"unchanged"`
	Pinned         bool     `json:"pinned"`
	Note           string   `json:"note"`
	Warnings       []string `json:"warnings"`
}
type SnapshotRun struct {
	ID         string            `json:"id"`
	TaskID     string            `json:"taskId"`
	TaskName   string            `json:"taskName"`
	StartedAt  string            `json:"startedAt"`
	FinishedAt string            `json:"finishedAt"`
	CatchUp    bool              `json:"catchUp"`
	Status     string            `json:"status"`
	Results    []SnapshotOutcome `json:"results"`
}
type SnapshotOutcome struct {
	Target         SnapshotTarget `json:"target"`
	ConnectionName string         `json:"connectionName"`
	SnapshotID     string         `json:"snapshotId"`
	Unchanged      bool           `json:"unchanged"`
	Error          string         `json:"error"`
	DurationMS     int64          `json:"durationMs"`
}
type SnapshotDocument struct {
	Record  SnapshotRecord `json:"record"`
	Archive SchemaArchive  `json:"archive"`
}
type SnapshotDiffRequest struct {
	BeforeID  string           `json:"beforeId"`
	AfterID   string           `json:"afterId"`
	Live      *SnapshotTarget  `json:"live,omitempty"`
	Normalize NormalizeOptions `json:"normalize"`
	Restore   bool             `json:"restore"`
}
type SnapshotDifference struct {
	Table  string       `json:"table"`
	Kind   string       `json:"kind"`
	Before string       `json:"before"`
	After  string       `json:"after"`
	Items  []ChangeItem `json:"items"`
}
type SnapshotDiff struct {
	Before      string               `json:"before"`
	After       string               `json:"after"`
	Differences []SnapshotDifference `json:"differences"`
	SQL         string               `json:"sql"`
	Warnings    []string             `json:"warnings"`
	SessionID   string               `json:"sessionId"`
}
type SnapshotExport struct {
	Filename string `json:"filename"`
	Content  string `json:"content"`
}
type SnapshotOverview struct {
	Tasks      []SnapshotTask   `json:"tasks"`
	Snapshots  []SnapshotRecord `json:"snapshots"`
	Runs       []SnapshotRun    `json:"runs"`
	Background bool             `json:"background"`
}
