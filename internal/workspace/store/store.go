// Package store keeps applied workspace files and their history in PostgreSQL.
package store

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/projection"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/spec"
)

//go:embed migrations/*.sql
var migrations embed.FS

const migrationLock int64 = 7263510713
const applyLockNamespace int32 = 72635107

var ErrApplyBusy = errors.New("another workspace apply is in progress")
var ErrVersionNotFound = errors.New("workspace version not found")

type Store struct{ database *postgreskit.Database }

type Version struct {
	Number    int64     `json:"version"`
	AppliedBy string    `json:"applied_by"`
	AppliedAt time.Time `json:"applied_at"`
	Document  []byte    `json:"-"`
}

type ExternalMapping struct {
	Source     string `json:"source"`
	Kind       string `json:"kind"`
	ExternalID string `json:"external_id"`
	Reference  string `json:"reference"`
	ResourceID string `json:"resource_id,omitempty"`
}

func (store *Store) Mappings(ctx context.Context, organizationID string) ([]ExternalMapping, error) {
	rows, err := store.database.Pool.Query(ctx, `SELECT source, kind, external_id, reference, coalesce(resource_id, '')
		FROM workspace.external_mappings WHERE organization_id=$1 ORDER BY source, kind, external_id`, organizationID)
	if err != nil {
		return nil, fmt.Errorf("read external mappings: %w", err)
	}
	defer rows.Close()
	result := []ExternalMapping{}
	for rows.Next() {
		var mapping ExternalMapping
		if err := rows.Scan(&mapping.Source, &mapping.Kind, &mapping.ExternalID, &mapping.Reference, &mapping.ResourceID); err != nil {
			return nil, fmt.Errorf("scan external mapping: %w", err)
		}
		result = append(result, mapping)
	}
	return result, rows.Err()
}

func New(database *postgreskit.Database) *Store { return &Store{database: database} }

func (store *Store) Migrate(ctx context.Context) error {
	return store.database.Migrate(ctx, "workspace", migrations, "migrations/*.sql", migrationLock)
}

// WithApplyLock holds a session advisory lock through provider API calls. A
// second process cannot apply to the same organization at the same time.
func (store *Store) WithApplyLock(ctx context.Context, organizationID string, operation func() error) error {
	connection, err := store.database.Pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire workspace apply connection: %w", err)
	}
	defer connection.Release()
	var locked bool
	if err := connection.QueryRow(ctx, `SELECT pg_try_advisory_lock($1, hashtext($2))`, applyLockNamespace, organizationID).Scan(&locked); err != nil {
		return fmt.Errorf("take workspace apply lock: %w", err)
	}
	if !locked {
		return ErrApplyBusy
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = connection.Exec(unlockCtx, `SELECT pg_advisory_unlock($1, hashtext($2))`, applyLockNamespace, organizationID)
	}()
	return operation()
}

func (store *Store) Latest(ctx context.Context, organizationID string) (Version, error) {
	var version Version
	err := store.database.Pool.QueryRow(ctx, `SELECT version, document, applied_by, applied_at
		FROM workspace.configs WHERE organization_id=$1`, organizationID).
		Scan(&version.Number, &version.Document, &version.AppliedBy, &version.AppliedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Version{}, ErrVersionNotFound
	}
	if err != nil {
		return Version{}, fmt.Errorf("read workspace version: %w", err)
	}
	return version, nil
}

func (store *Store) AtVersion(ctx context.Context, organizationID string, number int64) (Version, error) {
	var version Version
	err := store.database.Pool.QueryRow(ctx, `SELECT version, document, applied_by, applied_at
		FROM workspace.versions WHERE organization_id=$1 AND version=$2`, organizationID, number).
		Scan(&version.Number, &version.Document, &version.AppliedBy, &version.AppliedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Version{}, ErrVersionNotFound
	}
	if err != nil {
		return Version{}, fmt.Errorf("read workspace version: %w", err)
	}
	return version, nil
}

func (store *Store) Versions(ctx context.Context, organizationID string) ([]Version, error) {
	rows, err := store.database.Pool.Query(ctx, `SELECT version, applied_by, applied_at
		FROM workspace.versions WHERE organization_id=$1 ORDER BY version DESC LIMIT 100`, organizationID)
	if err != nil {
		return nil, fmt.Errorf("list workspace versions: %w", err)
	}
	defer rows.Close()
	versions := []Version{}
	for rows.Next() {
		var version Version
		if err := rows.Scan(&version.Number, &version.AppliedBy, &version.AppliedAt); err != nil {
			return nil, err
		}
		versions = append(versions, version)
	}
	return versions, rows.Err()
}

// Save is idempotent for identical files. The row lock serializes version
// numbers even if a caller bypasses WithApplyLock.
func (store *Store) Save(ctx context.Context, organizationID, actorID string, document []byte) (Version, error) {
	parsed, err := spec.Parse(document)
	if err != nil {
		return Version{}, fmt.Errorf("save invalid workspace file: %w", err)
	}
	projected, err := projection.FromDocument(parsed)
	if err != nil {
		return Version{}, fmt.Errorf("project workspace policy: %w", err)
	}
	settings, err := json.Marshal(projected)
	if err != nil {
		return Version{}, err
	}
	var result Version
	err = pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		var latest Version
		err := tx.QueryRow(ctx, `SELECT version, document, applied_by, applied_at
			FROM workspace.configs WHERE organization_id=$1 FOR UPDATE`, organizationID).
			Scan(&latest.Number, &latest.Document, &latest.AppliedBy, &latest.AppliedAt)
		if err == nil && bytes.Equal(latest.Document, document) {
			result = latest
			if _, err := tx.Exec(ctx, `UPDATE workspace.configs SET policy_settings=$2 WHERE organization_id=$1 AND policy_settings IS NULL`, organizationID, settings); err != nil {
				return err
			}
			return store.syncMappings(ctx, tx, organizationID, parsed.Directory)
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		result = Version{Number: latest.Number + 1, AppliedBy: actorID, AppliedAt: time.Now().UTC(), Document: document}
		if _, err := tx.Exec(ctx, `INSERT INTO workspace.configs
			(organization_id, version, document, applied_by, applied_at, policy_settings)
			VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (organization_id) DO UPDATE SET
			version=EXCLUDED.version, document=EXCLUDED.document,
			applied_by=EXCLUDED.applied_by, applied_at=EXCLUDED.applied_at, policy_settings=EXCLUDED.policy_settings`,
			organizationID, result.Number, result.Document, result.AppliedBy, result.AppliedAt, settings); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO workspace.versions
			(organization_id, version, document, applied_by, applied_at, policy_settings)
			VALUES ($1,$2,$3,$4,$5,$6)`,
			organizationID, result.Number, result.Document, result.AppliedBy, result.AppliedAt, settings)
		if err != nil {
			return err
		}
		return store.syncMappings(ctx, tx, organizationID, parsed.Directory)
	})
	if err != nil {
		return Version{}, fmt.Errorf("save workspace version: %w", err)
	}
	return result, nil
}

// syncMappings materializes the reviewed file in the same transaction as its
// version. Once an external ID is bound to a resource, it cannot be silently
// rebound to another Oryxa user or team on a later snapshot.
func (store *Store) syncMappings(ctx context.Context, tx pgx.Tx, organizationID string, directory *spec.Directory) error {
	wanted := map[string]bool{}
	upserts := []ExternalMapping{}
	if directory != nil {
		for _, identity := range directory.Identities {
			mapping := ExternalMapping{Source: identity.Source, Kind: "user", ExternalID: identity.ExternalID, Reference: identity.Member}
			wanted[mappingKey(mapping)] = true
			upserts = append(upserts, mapping)
		}
		for _, group := range directory.Groups {
			mapping := ExternalMapping{Source: group.Source, Kind: "team", ExternalID: group.ExternalID, Reference: group.Team}
			wanted[mappingKey(mapping)] = true
			upserts = append(upserts, mapping)
		}
	}
	rows, err := tx.Query(ctx, `SELECT source, kind, external_id FROM workspace.external_mappings WHERE organization_id=$1`, organizationID)
	if err != nil {
		return fmt.Errorf("list external mappings for pruning: %w", err)
	}
	var obsolete []ExternalMapping
	for rows.Next() {
		var mapping ExternalMapping
		if err := rows.Scan(&mapping.Source, &mapping.Kind, &mapping.ExternalID); err != nil {
			rows.Close()
			return err
		}
		if !wanted[mappingKey(mapping)] {
			obsolete = append(obsolete, mapping)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, mapping := range obsolete {
		if _, err := tx.Exec(ctx, `DELETE FROM workspace.external_mappings WHERE organization_id=$1 AND source=$2 AND kind=$3 AND external_id=$4`,
			organizationID, mapping.Source, mapping.Kind, mapping.ExternalID); err != nil {
			return fmt.Errorf("prune external mapping: %w", err)
		}
	}
	for _, mapping := range upserts {
		if err := store.upsertMapping(ctx, tx, organizationID, mapping); err != nil {
			return err
		}
	}
	return nil
}

func mappingKey(mapping ExternalMapping) string {
	return mapping.Source + "\x00" + mapping.Kind + "\x00" + mapping.ExternalID
}

func (store *Store) upsertMapping(ctx context.Context, tx pgx.Tx, organizationID string, mapping ExternalMapping) error {
	var resolved string
	var err error
	if mapping.Kind == "user" {
		err = tx.QueryRow(ctx, `SELECT m.user_id FROM auth_memberships m JOIN auth_users u ON u.id=m.user_id
			WHERE m.organization_id=$1 AND lower(u.email)=lower($2)`, organizationID, mapping.Reference).Scan(&resolved)
	} else {
		err = tx.QueryRow(ctx, `SELECT id FROM teams WHERE organization_id=$1 AND lower(name)=lower($2)`, organizationID, mapping.Reference).Scan(&resolved)
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("resolve external mapping: %w", err)
	}
	var existingResource string
	err = tx.QueryRow(ctx, `SELECT coalesce(resource_id, '') FROM workspace.external_mappings
		WHERE organization_id=$1 AND source=$2 AND kind=$3 AND external_id=$4`,
		organizationID, mapping.Source, mapping.Kind, mapping.ExternalID).Scan(&existingResource)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("read existing external mapping: %w", err)
	}
	if existingResource != "" {
		if resolved != "" && resolved != existingResource {
			return fmt.Errorf("external %s ID %q would change its Oryxa binding", mapping.Kind, mapping.ExternalID)
		}
		resolved = existingResource
	}
	_, err = tx.Exec(ctx, `INSERT INTO workspace.external_mappings
		(organization_id, source, kind, external_id, reference, resource_id, updated_at)
		VALUES ($1,$2,$3,$4,$5,nullif($6,''),now())
		ON CONFLICT (organization_id, source, kind, external_id) DO UPDATE SET
		reference=EXCLUDED.reference, resource_id=EXCLUDED.resource_id, updated_at=now()`,
		organizationID, mapping.Source, mapping.Kind, mapping.ExternalID, mapping.Reference, resolved)
	if err != nil {
		return fmt.Errorf("save external mapping: %w", err)
	}
	return nil
}
