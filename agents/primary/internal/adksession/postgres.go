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
		if err := Migrate(ctx, database); err != nil {
			return closeOnError(err)
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
	store, err := NewStore(database)
	if err != nil {
		return closeOnError(err)
	}
	return store, nil
}

// NewStore builds the session service over database, a pool whose
// search_path leads with agents_primary: ADK names its tables without a
// schema. It contacts nothing, so a service can assemble itself over a pool
// that has not connected yet. The store owns database from here on, and
// Close closes it.
func NewStore(database *postgreskit.Database) (*Store, error) {
	sessions, err := sessionService(database)
	if err != nil {
		return nil, err
	}
	return &Store{database: database, sessions: sessions}, nil
}

// Migrate creates the agents_primary schema and keeps ADK's tables current in
// it. database must lead its search_path with agents_primary, as NewStore's
// does: the schema has to exist before ADK creates its unqualified tables, or
// they would land in the next schema on the path.
func Migrate(ctx context.Context, database *postgreskit.Database) error {
	if _, err := database.SQLX.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS agents_primary`); err != nil {
		return fmt.Errorf("create ADK postgres schema: %w", err)
	}
	sessions, err := sessionService(database)
	if err != nil {
		return err
	}
	if err := adkdatabase.AutoMigrate(sessions); err != nil {
		return fmt.Errorf("migrate ADK sessions: %w", err)
	}
	return nil
}

func sessionService(database *postgreskit.Database) (session.Service, error) {
	gormDB, err := gorm.Open(postgres.New(postgres.Config{Conn: database.SQLX.DB}), &gorm.Config{
		DisableAutomaticPing: true,
		// PostgreSQL is instrumented below GORM. Silence GORM's SQL logger so a
		// failed event write cannot put conversation content in service logs.
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("open ADK session database: %w", err)
	}
	sessions, err := adkdatabase.NewSessionServiceFromDB(gormDB)
	if err != nil {
		return nil, fmt.Errorf("create ADK session service: %w", err)
	}
	return sessions, nil
}

// Sessions returns the persistent service passed to ADK's launcher or runner.
// A session created for a member starts with the member's state read from the
// auth tables over this store's own pool. When a SessionEvents is given, the
// service also reports session creation and deletion to it.
func (s *Store) Sessions(events ...SessionEvents) session.Service {
	service := seedingSessionService{Service: s.sessions, members: memberReader{database: s.database}}
	if len(events) > 0 {
		service.events = events[0]
	}
	return service
}

// Database returns the PostgreSQL pool this store owns, so the rest of the
// service, such as the events outbox, shares one connection budget.
func (s *Store) Database() *postgreskit.Database {
	return s.database
}

// Close releases the PostgreSQL pool.
func (s *Store) Close() {
	if s != nil {
		s.database.Close()
	}
}
