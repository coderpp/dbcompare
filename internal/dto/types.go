// Package dto defines JSON-serializable types shared by services and the frontend.
package dto

// ConnConfig is a MySQL server connection (password may be omitted when stored).
// Database is an optional default schema hint; compare/sync passes the active database separately.
type ConnConfig struct {
	SnapshotID string `json:"snapshotId,omitempty"`
	ID         string `json:"id"`
	Name       string `json:"name"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	User       string `json:"user"`
	Password   string `json:"password,omitempty"`
	Database   string `json:"database,omitempty"` // optional default database
	Params     string `json:"params,omitempty"`   // extra DSN params
}

// ServerInfo describes a live MySQL instance.
type ServerInfo struct {
	Version         string `json:"version"`
	VersionComment  string `json:"versionComment"`
	LowerCaseTables int    `json:"lowerCaseTableNames"`
	CharsetServer   string `json:"charsetServer"`
	CollationServer string `json:"collationServer"`
	HasSelectPriv   bool   `json:"hasSelectPriv"`
}

// NormalizeOptions controls cross-version noise suppression.
type NormalizeOptions struct {
	AutoRepairComments       bool `json:"autoRepairComments"` // opt-in: normalize reversible mojibake and include explicit repair plans
	StripIntDisplayWidth     bool `json:"stripIntDisplayWidth"`
	UnifyUTF8Aliases         bool `json:"unifyUtf8Aliases"`
	UnifyUTF8MB4CICollations bool `json:"unifyUtf8mb4CiCollations"` // general_ci ↔ 0900_ai_ci (compare only)
	IgnoreAutoIncrement      bool `json:"ignoreAutoIncrement"`
	IgnoreIndexOrder         bool `json:"ignoreIndexOrder"`
	IgnoreIndexes            bool `json:"ignoreIndexes"` // false preserves index comparison for older saved profiles
	IgnoreCommentsOnly       bool `json:"ignoreCommentsOnly"`
	CompareRowFormat         bool `json:"compareRowFormat"`
	HideGIPK                 bool `json:"hideGipk"`
}

// DefaultNormalizeOptions returns sensible defaults for 5.7↔8.0 compares.
func DefaultNormalizeOptions() NormalizeOptions {
	return NormalizeOptions{
		StripIntDisplayWidth:     true,
		UnifyUTF8Aliases:         true,
		UnifyUTF8MB4CICollations: true,
		IgnoreAutoIncrement:      true,
		IgnoreIndexOrder:         true,
		IgnoreCommentsOnly:       false,
		CompareRowFormat:         true,
		HideGIPK:                 true,
	}
}

// CompareRequest asks the engine to compare two schemas.
type CompareRequest struct {
	Source    ConnConfig       `json:"source"`
	Target    ConnConfig       `json:"target"`
	Tables    []string         `json:"tables,omitempty"`
	Normalize NormalizeOptions `json:"normalize"`
}

// ChangeKind classifies a schema change for UI tabs.
type ChangeKind string

const (
	KindAdd    ChangeKind = "add"
	KindModify ChangeKind = "modify"
	KindDrop   ChangeKind = "drop"
)

// RiskLevel grades destructive potential.
type RiskLevel string

const (
	RiskInfo     RiskLevel = "info"
	RiskWarn     RiskLevel = "warn"
	RiskError    RiskLevel = "error"
	RiskCritical RiskLevel = "critical"
)

// ChangeItem is one selectable unit in the diff UI.
type ChangeItem struct {
	ID         string     `json:"id"`
	Kind       ChangeKind `json:"kind"`
	Table      string     `json:"table"`
	ObjectType string     `json:"objectType"` // table|column|index|foreign_key|check|attr
	ObjectName string     `json:"objectName"`
	Summary    string     `json:"summary"`
	Detail     string     `json:"detail,omitempty"`
	Risk       RiskLevel  `json:"risk"`
	SQLPreview string     `json:"sqlPreview,omitempty"`
	Selected   bool       `json:"selected"`
}

// CompareResult is the full classified diff for the UI.
type CompareResult struct {
	SourceRepairs []CommentRepair `json:"sourceRepairs,omitempty"`
	TargetRepairs []CommentRepair `json:"targetRepairs,omitempty"`
	SourceInfo    ServerInfo      `json:"sourceInfo"`
	TargetInfo    ServerInfo      `json:"targetInfo"`
	Added         []ChangeItem    `json:"added"`
	Modified      []ChangeItem    `json:"modified"`
	Dropped       []ChangeItem    `json:"dropped"`
	Warnings      []string        `json:"warnings"`
	SessionID     string          `json:"sessionId"`
}

// PrecheckIssue is a feasibility finding before apply.
type PrecheckIssue struct {
	Level   RiskLevel `json:"level"`
	Code    string    `json:"code"`
	Message string    `json:"message"`
	Table   string    `json:"table,omitempty"`
}

// PlanRequest asks to generate DDL for selected changes.
type PlanRequest struct {
	SessionID   string   `json:"sessionId"`
	ChangeIDs   []string `json:"changeIds"`
	AllowDrop   bool     `json:"allowDrop"`
	AllowUnsafe bool     `json:"allowUnsafe"`
}

// PlannedStatement is one DDL statement ready to run.
type PlannedStatement struct {
	ID        string    `json:"id"`
	ChangeID  string    `json:"changeId"`
	ChangeIDs []string  `json:"changeIds"`
	Table     string    `json:"table"`
	SQL       string    `json:"sql"`
	Reverse   string    `json:"reverse,omitempty"`
	Risk      RiskLevel `json:"risk"`
	Comment   string    `json:"comment,omitempty"`
}

// PlanResult holds ordered DDL and precheck findings.
type PlanResult struct {
	SourceRepairPlan *PlanResult        `json:"sourceRepairPlan,omitempty"` // separately confirmed source database repairs, executed before this target plan
	TargetDatabase   string             `json:"targetDatabase"`
	ID               string             `json:"id"`
	SessionID        string             `json:"sessionId"`
	Selected         []ChangeItem       `json:"selected"`
	Snapshots        []TableSnapshot    `json:"snapshots"`
	AllowDrop        bool               `json:"allowDrop"`
	AllowUnsafe      bool               `json:"allowUnsafe"`
	Mode             string             `json:"mode"`
	Repairs          []CommentRepair    `json:"repairs,omitempty"`
	Statements       []PlannedStatement `json:"statements"`
	Prechecks        []PrecheckIssue    `json:"prechecks"`
	Blocked          bool               `json:"blocked"`
}

// ApplyRequest executes a previously planned sync.
type ApplyRequest struct {
	SessionID    string   `json:"sessionId"`
	PlanID       string   `json:"planId"`
	StatementIDs []string `json:"statementIds,omitempty"` // empty = all
	DryRun       bool     `json:"dryRun"`
}

// ApplyProgress reports live sync progress.
type ApplyProgress struct {
	Total      int    `json:"total"`
	Done       int    `json:"done"`
	CurrentSQL string `json:"currentSql"`
	Message    string `json:"message"`
	Failed     bool   `json:"failed"`
}

// ApplyResult is the final sync outcome including post-verify.
type ApplyResult struct {
	Run           *SyncRun `json:"run,omitempty"`
	Applied       int      `json:"applied"`
	Failed        int      `json:"failed"`
	Errors        []string `json:"errors"`
	VerifyClean   bool     `json:"verifyClean"`
	VerifySummary string   `json:"verifySummary"`
	Remaining     int      `json:"remaining"`
}

// SyncProfile is a saved sync scenario.
type SyncProfile struct {
	ID             string           `json:"id"`
	Name           string           `json:"name"`
	Description    string           `json:"description,omitempty"`
	SourceID       string           `json:"sourceId"`
	SourceDatabase string           `json:"sourceDatabase"`
	TargetID       string           `json:"targetId"`
	TargetDatabase string           `json:"targetDatabase"`
	Tables         []string         `json:"tables,omitempty"`
	Normalize      NormalizeOptions `json:"normalize"`
	AllowDrop      bool             `json:"allowDrop"`
	CreatedAt      string           `json:"createdAt"`
	UpdatedAt      string           `json:"updatedAt"`
}

// ColDiff represents a single column's before/after state for UI highlight.
type ColDiff struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"` // add | drop | modify | same
	Source string `json:"source,omitempty"`
	Target string `json:"target,omitempty"`
}

// TableDiffResult is a structured column/index comparison for one table.
type TableDiffResult struct {
	Table       string    `json:"table"`
	Attrs       []ColDiff `json:"attrs"`
	Columns     []ColDiff `json:"columns"`
	Indexes     []ColDiff `json:"indexes"`
	ForeignKeys []ColDiff `json:"foreignKeys"`
}

// AuditEntry records a DDL execution event.
type AuditEntry struct {
	ID         string `json:"id"`
	At         string `json:"at"`
	Action     string `json:"action"`
	SourceDSN  string `json:"sourceDsn"`
	TargetDSN  string `json:"targetDsn"`
	SQL        string `json:"sql"`
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
	DurationMs int64  `json:"durationMs"`
}

// Snapshot contains structure only, never table data or connection passwords.
type TableSnapshot struct {
	Table  string `json:"table"`
	Exists bool   `json:"exists"`
	DDL    string `json:"ddl"`
	SHA256 string `json:"sha256"`
}
type StatementOutcome struct {
	Statement  PlannedStatement `json:"statement"`
	Status     string           `json:"status"` // pending, running, success, failed, not_executed
	Error      string           `json:"error,omitempty"`
	DurationMs int64            `json:"durationMs"`
}
type RemainingDifference struct {
	Item     ChangeItem `json:"item"`
	Category string     `json:"category"` // selected, unselected, skipped_drop
}
type SyncRun struct {
	Tables            []string              `json:"tables,omitempty"`
	ID                string                `json:"id"`
	SessionID         string                `json:"sessionId"`
	PlanID            string                `json:"planId"`
	StartedAt         string                `json:"startedAt"`
	FinishedAt        string                `json:"finishedAt,omitempty"`
	Status            string                `json:"status"`
	Source            ConnConfig            `json:"source"`
	Target            ConnConfig            `json:"target"`
	Normalize         NormalizeOptions      `json:"normalize"`
	AllowDrop         bool                  `json:"allowDrop"`
	AllowUnsafe       bool                  `json:"allowUnsafe"`
	Mode              string                `json:"mode"`
	Selected          []ChangeItem          `json:"selected"`
	Outcomes          []StatementOutcome    `json:"outcomes"`
	Snapshots         []TableSnapshot       `json:"snapshots"`
	Remaining         []RemainingDifference `json:"remaining"`
	VerificationError string                `json:"verificationError,omitempty"`
	SelectionComplete bool                  `json:"selectionComplete"`
	Applied           int                   `json:"applied"`
	Failed            int                   `json:"failed"`
	Error             string                `json:"error,omitempty"`
	Repairs           []CommentRepair       `json:"repairs,omitempty"`
}
type CommentRepair struct {
	ID        string `json:"id"`
	Table     string `json:"table"`
	Column    string `json:"column"`
	Original  string `json:"original"`
	Recovered string `json:"recovered"`
}
type RepairRequest struct {
	SessionID string   `json:"sessionId"`
	Side      string   `json:"side"`
	IDs       []string `json:"ids"`
}
type ExportRequest struct {
	SessionID string `json:"sessionId,omitempty"`
	RunID     string `json:"runId,omitempty"`
	Kind      string `json:"kind"` // sql, snapshot, report, reverse
}
type ExportFile struct {
	Filename string `json:"filename"`
	Content  string `json:"content"`
}

type ResumeResult struct {
	Compare   *CompareResult `json:"compare"`
	ChangeIDs []string       `json:"changeIds"`
	RepairIDs []string       `json:"repairIds"`
	Mode      string         `json:"mode"`
}
