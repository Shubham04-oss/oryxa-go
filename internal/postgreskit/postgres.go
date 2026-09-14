// Package postgreskit owns the shared PostgreSQL client boundary used by the
// Go services. It keeps one native pgx pool, exposes a sqlx facade over that
// same pool, instruments pgx, and registers extension-specific wire types.
package postgreskit

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
	pgxvector "github.com/pgvector/pgvector-go/pgx"
	"go.opentelemetry.io/otel"
)

type Config struct {
	URL                string
	ApplicationName    string
	RequiredExtensions []string
}

type Database struct {
	Pool       *pgxpool.Pool
	SQLX       *sqlx.DB
	extensions map[string]string
}

func Open(ctx context.Context, config Config) (*Database, error) {
	if strings.TrimSpace(config.URL) == "" || strings.TrimSpace(config.ApplicationName) == "" {
		return nil, errors.New("postgres URL and application name are required")
	}
	poolConfig, err := pgxpool.ParseConfig(config.URL)
	if err != nil {
		// Parse errors may echo a URL containing credentials.
		return nil, errors.New("parse postgres configuration")
	}
	if poolConfig.ConnConfig.RuntimeParams["application_name"] == "" {
		poolConfig.ConnConfig.RuntimeParams["application_name"] = config.ApplicationName
	}
	if poolConfig.ConnConfig.ConnectTimeout == 0 {
		poolConfig.ConnConfig.ConnectTimeout = 5 * time.Second
	}
	if poolConfig.PingTimeout == 0 {
		poolConfig.PingTimeout = 5 * time.Second
	}
	poolConfig.ConnConfig.Tracer = otelpgx.NewTracer(
		otelpgx.WithTracerProvider(otel.GetTracerProvider()),
		otelpgx.WithMeterProvider(otel.GetMeterProvider()),
		otelpgx.WithTrimSQLInSpanName(),
		// Query parameters and full statements can contain tenant or provider
		// data. Operation-level spans and pool metrics are sufficient here.
		otelpgx.WithDisableSQLStatementInAttributes(),
	)
	previousAfterConnect := poolConfig.AfterConnect
	poolConfig.AfterConnect = func(ctx context.Context, connection *pgx.Conn) error {
		if previousAfterConnect != nil {
			if err := previousAfterConnect(ctx, connection); err != nil {
				return err
			}
		}
		var vectorInstalled bool
		if err := connection.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_extension WHERE extname = 'vector'
		)`).Scan(&vectorInstalled); err != nil {
			return fmt.Errorf("inspect pgvector extension: %w", err)
		}
		if vectorInstalled {
			if err := pgxvector.RegisterTypes(ctx, connection); err != nil {
				return fmt.Errorf("register pgvector types: %w", err)
			}
		}
		return nil
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("configure postgres pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	standardDB := stdlib.OpenDBFromPool(pool)
	sqlxDB := sqlx.NewDb(standardDB, "pgx")
	database := &Database{Pool: pool, SQLX: sqlxDB}
	database.extensions, err = loadExtensions(ctx, sqlxDB)
	if err != nil {
		database.Close()
		return nil, err
	}
	if err := database.RequireExtensions(config.RequiredExtensions...); err != nil {
		database.Close()
		return nil, err
	}
	if err := otelpgx.RecordStats(pool,
		otelpgx.WithStatsMeterProvider(otel.GetMeterProvider()),
		otelpgx.WithMinimumReadDBStatsInterval(15*time.Second)); err != nil {
		database.Close()
		return nil, fmt.Errorf("register postgres pool metrics: %w", err)
	}
	return database, nil
}

func (database *Database) Close() {
	if database == nil {
		return
	}
	if database.SQLX != nil {
		_ = database.SQLX.Close()
	}
	if database.Pool != nil {
		database.Pool.Close()
	}
}

func (database *Database) ExtensionVersions() map[string]string {
	if database == nil {
		return map[string]string{}
	}
	return maps.Clone(database.extensions)
}

func (database *Database) RequireExtensions(names ...string) error {
	missing := make([]string, 0, len(names))
	for _, name := range names {
		name = normalizeExtensionName(name)
		if name != "" {
			if _, installed := database.extensions[name]; !installed {
				missing = append(missing, name)
			}
		}
	}
	slices.Sort(missing)
	missing = slices.Compact(missing)
	if len(missing) != 0 {
		return fmt.Errorf("required postgres extensions are not installed: %s", strings.Join(missing, ", "))
	}
	return nil
}

func normalizeExtensionName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	switch name {
	case "pgvector":
		return "vector"
	case "apache age", "apache_age", "apache-age", "apacheage":
		return "age"
	default:
		return name
	}
}

type extensionRow struct {
	Name    string `db:"name"`
	Version string `db:"version"`
}

func loadExtensions(ctx context.Context, database *sqlx.DB) (map[string]string, error) {
	var rows []extensionRow
	if err := database.SelectContext(ctx, &rows, `
		SELECT extname AS name, extversion AS version
		FROM pg_extension
		WHERE extname IN ('age', 'ltree', 'pg_trgm', 'vector')
		ORDER BY extname`); err != nil {
		return nil, fmt.Errorf("discover postgres extensions: %w", err)
	}
	result := make(map[string]string, len(rows))
	for _, row := range rows {
		result[row.Name] = row.Version
	}
	return result, nil
}
