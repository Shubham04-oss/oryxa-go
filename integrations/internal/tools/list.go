package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// Offered is one tool a member can be offered, as its connector's manifest
// declares it.
type Offered struct {
	App         string          `json:"app"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Effect      string          `json:"effect"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// List answers the tools of the apps a member's live connections use (their
// own and the org-level ones, as a call may use), app by app and in each
// manifest's order; with app, that app's only. An app whose connector is not
// running offers nothing until it is.
func (s *Service) List(ctx context.Context, org, member, app string) ([]Offered, error) {
	for _, f := range []struct{ name, value string }{{"org", org}, {"member", member}} {
		if !idRE.MatchString(f.value) {
			return nil, fmt.Errorf("%w: %s must be 1 to 128 of letters, digits and . _ : @ + = -", ErrInvalid, f.name)
		}
	}
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT app FROM integrations.connections
		WHERE org_id = $1 AND status <> 'revoked' AND (level = 'org' OR member_id = $2) AND ($3 = '' OR app = $3)
		ORDER BY app`, org, member, app)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var apps []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		apps = append(apps, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	offered := []Offered{}
	for _, id := range apps {
		m, ok := s.connectors.Manifest(id)
		if !ok {
			continue
		}
		for _, t := range m.Tools {
			offered = append(offered, Offered{App: id, Name: t.Name, Description: t.Description, Effect: t.Effect, InputSchema: t.InputSchema})
		}
	}
	return offered, nil
}
