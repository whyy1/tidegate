package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// dbPragmas are applied to every pooled connection: WAL for concurrent
// readers, a busy timeout instead of immediate SQLITE_BUSY errors, foreign
// keys for cascading deletes, and immediate transactions so that a write
// transaction never fails when upgrading its lock.
const dbPragmas = "_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_txlock=immediate"

// DBFileName is the name of the database file in the data directory.
const DBFileName = "tidegate.db"

// Migration is one schema change of a component.
type Migration struct {
	// Version orders the migrations of one component. It starts at 1 and
	// increases; applied versions are recorded in schema_migrations.
	Version int
	Name    string
	// SQL runs inside the migration's transaction; it may contain several
	// statements.
	SQL string
	// Func, when set, runs after SQL in the same transaction.
	Func func(ctx context.Context, tx *sql.Tx) error
}

// ResultsComponent is the migration component of the result tables.
const ResultsComponent = "results"

var (
	registryMu sync.Mutex
	registry   = map[string][]Migration{}
)

// RegisterMigrations adds migrations of a component (for example "auth").
// OpenDB applies the migrations of every registered component, so packages
// register from an init function. Migrate applies migrations directly.
func RegisterMigrations(component string, ms ...Migration) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[component] = append(registry[component], ms...)
}

func init() {
	RegisterMigrations(ResultsComponent, resultMigrations...)
}

// OpenDB opens (creating if needed) the SQLite database at path and applies
// all registered migrations.
func OpenDB(path string) (*sql.DB, error) {
	if strings.ContainsRune(path, '?') {
		return nil, errors.New("数据库路径不能包含问号")
	}
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return nil, fmt.Errorf("创建数据目录失败：%w", err)
	}
	db, err := sql.Open("sqlite", path+"?"+dbPragmas)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败：%w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("打开数据库失败：%w", err)
	}
	if err := MigrateRegistered(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// MigrateRegistered applies the migrations of every registered component:
// the result tables first, then the other components by name.
func MigrateRegistered(ctx context.Context, db *sql.DB) error {
	registryMu.Lock()
	comps := make([]string, 0, len(registry))
	all := make(map[string][]Migration, len(registry))
	for c, ms := range registry {
		comps = append(comps, c)
		all[c] = append([]Migration(nil), ms...)
	}
	registryMu.Unlock()
	sort.Slice(comps, func(i, j int) bool {
		if (comps[i] == ResultsComponent) != (comps[j] == ResultsComponent) {
			return comps[i] == ResultsComponent
		}
		return comps[i] < comps[j]
	})
	for _, c := range comps {
		if err := Migrate(ctx, db, c, all[c]); err != nil {
			return err
		}
	}
	return nil
}

const createMigrationsTable = `CREATE TABLE IF NOT EXISTS schema_migrations (
	component  TEXT    NOT NULL,
	version    INTEGER NOT NULL,
	name       TEXT    NOT NULL,
	applied_at INTEGER NOT NULL,
	PRIMARY KEY (component, version)
)`

// Migrate applies the migrations of one component that are not applied
// yet, each in its own transaction. It refuses to run when the database
// has a newer version than the program knows.
func Migrate(ctx context.Context, db *sql.DB, component string, ms []Migration) error {
	if component == "" {
		return errors.New("store: migration component must not be empty")
	}
	ms = append([]Migration(nil), ms...)
	sort.Slice(ms, func(i, j int) bool { return ms[i].Version < ms[j].Version })
	for i, m := range ms {
		if m.Version < 1 {
			return fmt.Errorf("store: migration %s/%q has version %d, versions start at 1", component, m.Name, m.Version)
		}
		if i > 0 && ms[i-1].Version == m.Version {
			return fmt.Errorf("store: duplicate migration version %s/%d", component, m.Version)
		}
	}
	if _, err := db.ExecContext(ctx, createMigrationsTable); err != nil {
		return fmt.Errorf("初始化数据库失败：%w", err)
	}
	var current int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations WHERE component = ?`, component).Scan(&current); err != nil {
		return fmt.Errorf("读取数据库版本失败：%w", err)
	}
	latest := 0
	if len(ms) > 0 {
		latest = ms[len(ms)-1].Version
	}
	if current > latest {
		return fmt.Errorf("数据库中 %s 的结构版本是 %d，比当前程序支持的 %d 新，请升级程序", component, current, latest)
	}
	for _, m := range ms {
		if m.Version <= current {
			continue
		}
		if err := applyMigration(ctx, db, component, m); err != nil {
			return fmt.Errorf("数据库升级失败（%s 第 %d 步 %s）：%w", component, m.Version, m.Name, err)
		}
	}
	return nil
}

func applyMigration(ctx context.Context, db *sql.DB, component string, m Migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Another process may have applied it since the version was read; the
	// immediate transaction holds the write lock, so this check is final.
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE component = ? AND version = ?`, component, m.Version).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	if strings.TrimSpace(m.SQL) != "" {
		if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
			return err
		}
	}
	if m.Func != nil {
		if err := m.Func(ctx, tx); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (component, version, name, applied_at) VALUES (?, ?, ?, ?)`,
		component, m.Version, m.Name, time.Now().UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}

// isForeignKeyError reports a violated foreign key constraint.
func isForeignKeyError(err error) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code() == sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY
}
