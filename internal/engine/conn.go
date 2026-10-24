package engine

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"ariga.io/atlas/sql/migrate"
	"ariga.io/atlas/sql/mysql"
	"ariga.io/atlas/sql/schema"
	_ "github.com/go-sql-driver/mysql"

	"github.com/pp/db-compare/internal/dto"
)

// Conn wraps a live MySQL connection and its Atlas driver.
type Conn struct {
	ArchiveModel string
	DB           *sql.DB
	Driver       migrate.Driver
	Config       dto.ConnConfig
	Info         dto.ServerInfo
	SemVer       SemVer
}

// SemVer is a parsed MySQL server version.
type SemVer struct {
	Major int
	Minor int
	Patch int
	Raw   string
}

func (v SemVer) AtLeast(major, minor, patch int) bool {
	if v.Major != major {
		return v.Major > major
	}
	if v.Minor != minor {
		return v.Minor > minor
	}
	return v.Patch >= patch
}

func (v SemVer) Is57() bool { return v.Major == 5 && v.Minor == 7 }
func (v SemVer) Is80() bool { return v.Major == 8 }

var versionRe = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// ParseVersion extracts major.minor.patch from @@version.
func ParseVersion(raw string) SemVer {
	m := versionRe.FindStringSubmatch(raw)
	if m == nil {
		return SemVer{Raw: raw}
	}
	maj, _ := strconv.Atoi(m[1])
	min, _ := strconv.Atoi(m[2])
	pat, _ := strconv.Atoi(m[3])
	return SemVer{Major: maj, Minor: min, Patch: pat, Raw: raw}
}

// BuildDSN builds a go-sql-driver DSN from ConnConfig.
func BuildDSN(c dto.ConnConfig) string {
	params := url.Values{}
	params.Set("parseTime", "true")
	params.Set("loc", "Local")
	params.Set("charset", "utf8mb4")
	params.Set("timeout", "10s")
	params.Set("readTimeout", "60s")
	params.Set("writeTimeout", "60s")
	if c.Params != "" {
		extra, err := url.ParseQuery(strings.TrimPrefix(c.Params, "?"))
		if err == nil {
			for k, vs := range extra {
				for _, v := range vs {
					params.Set(k, v)
				}
			}
		}
	}
	user := url.UserPassword(c.User, c.Password)
	host := c.Host
	if c.Port <= 0 {
		c.Port = 3306
	}
	return fmt.Sprintf("%s@tcp(%s:%d)/%s?%s", user.String(), host, c.Port, c.Database, params.Encode())
}

// MaskDSN redacts password for logs/audit.
func MaskDSN(c dto.ConnConfig) string {
	cp := c
	if cp.Password != "" {
		cp.Password = "***"
	}
	return BuildDSN(cp)
}

// OpenServer connects to a MySQL instance without selecting a schema.
func OpenServer(ctx context.Context, cfg dto.ConnConfig) (*Conn, error) {
	if cfg.Host == "" || cfg.User == "" {
		return nil, fmt.Errorf("host and user are required")
	}
	return openConn(ctx, cfg, false)
}

// Open connects to a schema, probes server info, opens Atlas driver, and validates privileges.
func Open(ctx context.Context, cfg dto.ConnConfig) (*Conn, error) {
	if cfg.Host == "" || cfg.User == "" || cfg.Database == "" {
		return nil, fmt.Errorf("host, user and database are required")
	}
	return openConn(ctx, cfg, true)
}

func openConn(ctx context.Context, cfg dto.ConnConfig, checkSchema bool) (*Conn, error) {
	db, err := sql.Open("mysql", BuildDSN(cfg))
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(5 * time.Minute)

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	dbLabel := cfg.Database
	if dbLabel == "" {
		dbLabel = "(server)"
	}
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping %s@%s:%d/%s: %w", cfg.User, cfg.Host, cfg.Port, dbLabel, err)
	}

	info, sem, err := probeServer(ctx, db)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if checkSchema {
		if err := checkPrivileges(ctx, db, cfg.Database); err != nil {
			_ = db.Close()
			return nil, err
		}
	}

	if sem.AtLeast(8, 0, 30) {
		// Hide generated invisible primary keys from I_S / SHOW CREATE.
		_, _ = db.ExecContext(ctx, "SET SESSION show_gipk_in_create_table_and_information_schema = OFF")
	}

	drv, err := mysql.Open(db)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("atlas mysql open: %w", err)
	}

	return &Conn{DB: db, Driver: drv, Config: cfg, Info: info, SemVer: sem}, nil
}

// ListDatabases returns user-visible schemas on the server.
func ListDatabases(ctx context.Context, cfg dto.ConnConfig) ([]string, error) {
	conn, err := OpenServer(ctx, cfg)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	rows, err := conn.DB.QueryContext(ctx, `
SELECT schema_name
FROM information_schema.schemata
WHERE schema_name NOT IN ('information_schema','mysql','performance_schema','sys')
ORDER BY schema_name`)
	if err != nil {
		return nil, fmt.Errorf("list databases: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

func (c *Conn) Close() error {
	if c == nil || c.DB == nil {
		return nil
	}
	return c.DB.Close()
}

func probeServer(ctx context.Context, db *sql.DB) (dto.ServerInfo, SemVer, error) {
	var info dto.ServerInfo
	row := db.QueryRowContext(ctx, `
SELECT
  @@version,
  @@version_comment,
  @@lower_case_table_names,
  @@character_set_server,
  @@collation_server`)
	if err := row.Scan(&info.Version, &info.VersionComment, &info.LowerCaseTables, &info.CharsetServer, &info.CollationServer); err != nil {
		return info, SemVer{}, fmt.Errorf("probe server vars: %w", err)
	}
	info.HasSelectPriv = true
	return info, ParseVersion(info.Version), nil
}

func checkPrivileges(ctx context.Context, db *sql.DB, database string) error {
	// Ensure we can read information_schema for the target schema.
	var n int
	err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM information_schema.tables
WHERE table_schema = ?`, database).Scan(&n)
	if err != nil {
		return fmt.Errorf("privilege check failed (need SELECT on information_schema): %w", err)
	}
	return nil
}

// Inspect reads schema metadata for the connected database.
func (c *Conn) Inspect(ctx context.Context, tables []string) (*schema.Schema, error) {
	if c.ArchiveModel != "" {
		return ReadArchiveModel(c.ArchiveModel)
	}
	opts := &schema.InspectOptions{
		Mode: schema.InspectTables,
	}
	if len(tables) > 0 {
		opts.Tables = tables
	}
	s, err := c.Driver.InspectSchema(ctx, c.Config.Database, opts)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", c.Config.Database, err)
	}
	return s, nil
}

// CheckLowerCaseConsistency warns when source and target disagree.
func CheckLowerCaseConsistency(src, dst dto.ServerInfo) string {
	if src.LowerCaseTables != dst.LowerCaseTables {
		return fmt.Sprintf(
			"lower_case_table_names differs (source=%d, target=%d); table name matching may produce false add/drop pairs",
			src.LowerCaseTables, dst.LowerCaseTables,
		)
	}
	return ""
}
