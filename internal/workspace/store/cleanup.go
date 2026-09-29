package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"go.yaml.in/yaml/v3"
)

// Departure contains only the fields needed to forget a departed resource.
// The event consumer verifies the publisher's signature before calling Cleanup.
type Departure struct {
	EventID, OrganizationID, Kind, ResourceID, Reference string
	OccurredAt                                           time.Time
}

// Cleanup serializes with apply, atomically recording a receipt with the current
// document change. Redelivery cannot erase an identity rebound by a later apply.
func (store *Store) Cleanup(ctx context.Context, event Departure) error {
	if event.EventID == "" || event.OrganizationID == "" || event.OccurredAt.IsZero() ||
		(event.Kind != "organization" && (event.ResourceID == "" || (event.Kind != "user" && event.Kind != "team"))) {
		return errors.New("invalid workspace departure")
	}
	return store.WithApplyLock(ctx, event.OrganizationID, func() error {
		return pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
			if event.Kind == "organization" {
				if _, err := tx.Exec(ctx, `INSERT INTO workspace.deleted_organizations (organization_id,deleted_at)
                    VALUES ($1,$2) ON CONFLICT (organization_id) DO NOTHING`, event.OrganizationID, event.OccurredAt); err != nil {
					return err
				}
				// History is retained only while the organization exists.
				for _, table := range []string{"reviewed_plans", "apply_attempts", "external_mappings", "versions", "configs", "lifecycle_receipts"} {
					if _, err := tx.Exec(ctx, `DELETE FROM workspace.`+table+` WHERE organization_id=$1`, event.OrganizationID); err != nil {
						return err
					}
				}
				return nil
			}
			var deleted bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM workspace.deleted_organizations WHERE organization_id=$1)`, event.OrganizationID).Scan(&deleted); err != nil {
				return err
			}
			if deleted {
				return nil
			}
			receipt, err := tx.Exec(ctx, `INSERT INTO workspace.lifecycle_receipts (event_id,organization_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, event.EventID, event.OrganizationID)
			if err != nil || receipt.RowsAffected() == 0 {
				return err
			}
			refs := map[string]bool{}
			rows, err := tx.Query(ctx, `DELETE FROM workspace.external_mappings WHERE organization_id=$1 AND kind=$2 AND resource_id=$3 RETURNING reference`, event.OrganizationID, event.Kind, event.ResourceID)
			if err != nil {
				return err
			}
			for rows.Next() {
				var ref string
				if err := rows.Scan(&ref); err != nil {
					rows.Close()
					return err
				}
				refs[normalizeReference(ref)] = true
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			var latest Version
			var settings []byte
			err = tx.QueryRow(ctx, `SELECT version,document,applied_by,applied_at,policy_settings FROM workspace.configs WHERE organization_id=$1 FOR UPDATE`, event.OrganizationID).Scan(&latest.Number, &latest.Document, &latest.AppliedBy, &latest.AppliedAt, &settings)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			found := err == nil
			intentAt := latest.AppliedAt
			if found && latest.AppliedBy == "system:lifecycle" {
				// A previous cleanup did not renew the administrator's intent.
				// Use the latest real apply so out-of-order departures still
				// remove facts from the same old manifest.
				err := tx.QueryRow(ctx, `SELECT applied_at FROM workspace.versions WHERE organization_id=$1 AND applied_by<>'system:lifecycle' ORDER BY version DESC LIMIT 1`, event.OrganizationID).Scan(&intentAt)
				if err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return err
				}
			}
			// An email/name alone cannot identify a recreated resource in a
			// manifest applied after this departure. Only stable-ID bindings
			// deleted above establish that a newer fact belongs to the old one.
			if found && !intentAt.After(event.OccurredAt) {
				if ref := normalizeReference(event.Reference); ref != "" {
					refs[ref] = true
				}
			}
			// A deleted team name may have been reused for another stable ID.
			// Its new binding must survive a delayed event for the old team.
			rows, err = tx.Query(ctx, `SELECT reference FROM workspace.external_mappings WHERE organization_id=$1 AND kind=$2 AND resource_id IS NOT NULL AND resource_id<>$3`, event.OrganizationID, event.Kind, event.ResourceID)
			if err != nil {
				return err
			}
			for rows.Next() {
				var ref string
				if err := rows.Scan(&ref); err != nil {
					rows.Close()
					return err
				}
				delete(refs, normalizeReference(ref))
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			if found {
				document, err := forgetReferences(latest.Document, event.Kind, refs)
				if err != nil {
					return fmt.Errorf("forget workspace references: %w", err)
				}
				// Even a mapping-only change invalidates outstanding plan versions.
				now := time.Now().UTC()
				if _, err := tx.Exec(ctx, `UPDATE workspace.configs SET version=$2,document=$3,applied_by=$4,applied_at=$5 WHERE organization_id=$1`, event.OrganizationID, latest.Number+1, document, "system:lifecycle", now); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `INSERT INTO workspace.versions (organization_id,version,document,applied_by,applied_at,policy_settings) VALUES ($1,$2,$3,$4,$5,$6)`, event.OrganizationID, latest.Number+1, document, "system:lifecycle", now, settings); err != nil {
					return err
				}
			}
			_, err = tx.Exec(ctx, `DELETE FROM workspace.reviewed_plans WHERE organization_id=$1`, event.OrganizationID)
			return err
		})
	})
}

// PurgeExpiredPlans runs independently of incoming events. Short-lived approval
// payloads must not retain directory details indefinitely after they expire.
func (store *Store) PurgeExpiredPlans(ctx context.Context) error {
	return pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM workspace.reviewed_plans WHERE expires_at<=now()`); err != nil {
			return err
		}
		// A plan or progress write already in flight during deletion may
		// finish after its first purge; repeat payload removal periodically.
		for _, table := range []string{"reviewed_plans", "apply_attempts", "external_mappings", "versions", "configs", "lifecycle_receipts"} {
			if _, err := tx.Exec(ctx, `DELETE FROM workspace.`+table+` WHERE organization_id IN (SELECT organization_id FROM workspace.deleted_organizations)`); err != nil {
				return err
			}
		}
		// Signatures expire well before these opaque deduplication receipts do.
		_, err := tx.Exec(ctx, `DELETE FROM workspace.lifecycle_receipts WHERE handled_at<now()-interval '30 days'`)
		return err
	})
}

func normalizeReference(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

// Edit YAML nodes instead of round-tripping through the current spec, preserving
// fields introduced by a newer server during a rolling deployment.
func forgetReferences(document []byte, kind string, refs map[string]bool) ([]byte, error) {
	var node yaml.Node
	if err := yaml.Unmarshal(document, &node); err != nil {
		return nil, err
	}
	if len(node.Content) != 1 || node.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("workspace document must be a mapping")
	}
	root := node.Content[0]
	matches := func(value string) bool { return refs[normalizeReference(value)] }
	changed := false
	filter := func(sequence *yaml.Node, drop func(*yaml.Node) bool) {
		if sequence == nil || sequence.Kind != yaml.SequenceNode {
			return
		}
		kept := sequence.Content[:0]
		for _, item := range sequence.Content {
			if drop(item) {
				changed = true
			} else {
				kept = append(kept, item)
			}
		}
		sequence.Content = kept
	}
	if kind == "user" {
		filter(field(field(root, "people"), "members"), func(item *yaml.Node) bool { return matches(value(item, "email")) })
		for _, team := range items(field(root, "teams")) {
			for _, key := range []string{"members", "leads"} {
				filter(field(team, key), func(item *yaml.Node) bool { return matches(item.Value) })
			}
		}
		filter(field(field(root, "directory"), "identities"), func(item *yaml.Node) bool { return matches(value(item, "member")) })
		chart := field(root, "orgChart")
		filter(field(chart, "relationships"), func(item *yaml.Node) bool { return matches(value(item, "from")) || matches(value(item, "to")) })
		filter(field(chart, "executives"), func(item *yaml.Node) bool { return matches(item.Value) })
		ownership := field(root, "ownership")
		if ownership != nil && ownership.Kind == yaml.MappingNode {
			ownership = field(ownership, "items")
		}
		filter(ownership, func(item *yaml.Node) bool { return matches(value(item, "owner")) })
	} else {
		teams := field(root, "teams")
		if teams != nil && teams.Kind == yaml.MappingNode {
			teams = field(teams, "items")
		}
		filter(teams, func(item *yaml.Node) bool { return matches(value(item, "name")) })
		filter(field(field(root, "directory"), "groups"), func(item *yaml.Node) bool { return matches(value(item, "team")) })
	}
	if !changed {
		return document, nil
	}
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(&node); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
func field(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}
func value(node *yaml.Node, key string) string {
	if child := field(node, key); child != nil {
		return child.Value
	}
	return ""
}
func items(node *yaml.Node) []*yaml.Node {
	if node == nil {
		return nil
	}
	if node.Kind == yaml.MappingNode {
		node = field(node, "items")
	}
	if node == nil || node.Kind != yaml.SequenceNode {
		return nil
	}
	return node.Content
}
