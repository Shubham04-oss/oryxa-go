// Package adksession connects ADK's conversation sessions and state to the
// persistence used by agents-primary.
package adksession

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/adk/v2/session"
	adkdatabase "google.golang.org/adk/v2/session/database"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

const postgresSchema = "agents_primary"

// Store owns the PostgreSQL pool and the ADK session service backed by it.
type Store struct {
	database *postgreskit.Database
	sessions session.Service
}

// Open connects to PostgreSQL and creates ADK's database-backed session
// service. When autoMigrate is true, ADK keeps its tables current inside the
// agents_primary schema.
func Open(ctx context.Context, databaseURL string, autoMigrate bool) (*Store, error) {
	if databaseURL == "" {
		return nil, errors.New("DATABASE_URL is required for ADK session persistence")
	}
	database, err := postgreskit.Open(ctx, postgreskit.Config{
		URL: databaseURL, ApplicationName: "oryxa-agents-primary", Schema: postgresSchema,
	})
	if err != nil {
		return nil, fmt.Errorf("open agents postgres: %w", err)
	}
	closeOnError := func(err error) (*Store, error) {
		database.Close()
		return nil, err
	}

	if autoMigrate {
		if _, err := database.SQLX.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS agents_primary`); err != nil {
			return closeOnError(fmt.Errorf("create ADK postgres schema: %w", err))
		}
	} else {
		var exists bool
		if err := database.SQLX.GetContext(ctx, &exists, `SELECT EXISTS (
			SELECT 1 FROM pg_namespace WHERE nspname = 'agents_primary'
		)`); err != nil {
			return closeOnError(fmt.Errorf("inspect ADK postgres schema: %w", err))
		}
		if !exists {
			return closeOnError(errors.New("ADK postgres schema agents_primary does not exist and AGENTS_PRIMARY_AUTO_MIGRATE is false"))
		}
	}

	gormDB, err := gorm.Open(postgres.New(postgres.Config{Conn: database.SQLX.DB}), &gorm.Config{
		DisableAutomaticPing: true,
		// PostgreSQL is instrumented below GORM. Silence GORM's SQL logger so a
		// failed event write cannot put conversation content in service logs.
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return closeOnError(fmt.Errorf("open ADK session database: %w", err))
	}
	sessions, err := adkdatabase.NewSessionServiceFromDB(gormDB)
	if err != nil {
		return closeOnError(fmt.Errorf("create ADK session service: %w", err))
	}
	if autoMigrate {
		if err := adkdatabase.AutoMigrate(sessions); err != nil {
			return closeOnError(fmt.Errorf("migrate ADK sessions: %w", err))
		}
	}
	return &Store{database: database, sessions: sessions}, nil
}

// Sessions returns the persistent service passed to ADK's launcher or runner.
func (s *Store) Sessions() session.Service {
	return s.sessions
}

// Close releases the PostgreSQL pool.
func (s *Store) Close() {
	if s != nil {
		s.database.Close()
	}
}
