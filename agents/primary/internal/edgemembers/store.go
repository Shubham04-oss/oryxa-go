package edgemembers

import (
	"context"
	"embed"
	"errors"
	"fmt"

	edgev1 "github.com/Shubham04-oss/oryxa-go/contracts/edge/v1"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

//go:embed migrations/*.sql
var migrations embed.FS

// migrationsAdvisoryLock is this store's own, from the 72635107xx range the
// agents' stores share; the highest taken before it was secretshares'
// 7263510721.
const migrationsAdvisoryLock int64 = 7263510722

// Membership is one member of one room, as pushed.
type Membership struct {
	Org         string
	Room        edgev1.Room
	Member      string
	TeamspaceID string
}

// Store remembers what Pusher pushed.
type Store struct {
	database *postgreskit.Database
}

// NewStore returns a Store on database.
func NewStore(database *postgreskit.Database) *Store {
	return &Store{database: database}
}

// Migrate creates the table. It is safe to repeat.
func (store *Store) Migrate(ctx context.Context) error {
	return store.database.Migrate(ctx, "agents-edge-room-members", migrations, "migrations/*.sql", migrationsAdvisoryLock)
}

// Record keeps membership, once however often it is pushed.
func (store *Store) Record(ctx context.Context, membership Membership) error {
	if store == nil || store.database == nil {
		return errors.New("edge room members need a database")
	}
	_, err := store.database.Pool.Exec(ctx, `
		INSERT INTO agents_primary.edge_room_members (organization_id, room_kind, room_id, member_id, teamspace_id)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (organization_id, room_kind, room_id, member_id) DO NOTHING`,
		membership.Org, string(membership.Room.Kind), membership.Room.ID, membership.Member, membership.TeamspaceID)
	if err != nil {
		return fmt.Errorf("record an edge room member: %w", err)
	}
	return nil
}

// Forget drops one membership.
func (store *Store) Forget(ctx context.Context, membership Membership) error {
	_, err := store.database.Pool.Exec(ctx, `
		DELETE FROM agents_primary.edge_room_members
		WHERE organization_id = $1 AND room_kind = $2 AND room_id = $3 AND member_id = $4`,
		membership.Org, string(membership.Room.Kind), membership.Room.ID, membership.Member)
	if err != nil {
		return fmt.Errorf("forget an edge room member: %w", err)
	}
	return nil
}

// Rooms lists the memberships of member in org: every room, or, when
// teamspaceIDs is not empty, the rooms of those teamspaces only.
func (store *Store) Rooms(ctx context.Context, org, member string, teamspaceIDs []string) ([]Membership, error) {
	query := `
		SELECT room_kind, room_id, teamspace_id FROM agents_primary.edge_room_members
		WHERE organization_id = $1 AND member_id = $2`
	arguments := []any{org, member}
	if len(teamspaceIDs) > 0 {
		query += ` AND teamspace_id = ANY($3)`
		arguments = append(arguments, teamspaceIDs)
	}
	rows, err := store.database.Pool.Query(ctx, query+` ORDER BY room_kind, room_id`, arguments...)
	if err != nil {
		return nil, fmt.Errorf("list a member's edge rooms: %w", err)
	}
	defer rows.Close()
	var memberships []Membership
	for rows.Next() {
		membership := Membership{Org: org, Member: member}
		var kind string
		if err := rows.Scan(&kind, &membership.Room.ID, &membership.TeamspaceID); err != nil {
			return nil, err
		}
		membership.Room.Kind = edgev1.RoomKind(kind)
		memberships = append(memberships, membership)
	}
	return memberships, rows.Err()
}

// Exists reports whether membership is still kept: a removal that ran in
// the meantime forgot it.
func (store *Store) Exists(ctx context.Context, membership Membership) (bool, error) {
	var exists bool
	err := store.database.Pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM agents_primary.edge_room_members
		WHERE organization_id = $1 AND room_kind = $2 AND room_id = $3 AND member_id = $4)`,
		membership.Org, string(membership.Room.Kind), membership.Room.ID, membership.Member).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check an edge room member: %w", err)
	}
	return exists, nil
}

// InTeamspace lists every membership of a teamspace's rooms: its room and
// its side conversations, whoever the member.
func (store *Store) InTeamspace(ctx context.Context, org, teamspaceID string) ([]Membership, error) {
	rows, err := store.database.Pool.Query(ctx, `
		SELECT room_kind, room_id, member_id FROM agents_primary.edge_room_members
		WHERE organization_id = $1 AND teamspace_id = $2
		ORDER BY room_kind, room_id, member_id`, org, teamspaceID)
	if err != nil {
		return nil, fmt.Errorf("list a teamspace's edge rooms: %w", err)
	}
	defer rows.Close()
	var memberships []Membership
	for rows.Next() {
		membership := Membership{Org: org, TeamspaceID: teamspaceID}
		var kind string
		if err := rows.Scan(&kind, &membership.Room.ID, &membership.Member); err != nil {
			return nil, err
		}
		membership.Room.Kind = edgev1.RoomKind(kind)
		memberships = append(memberships, membership)
	}
	return memberships, rows.Err()
}

// ForgetOrganization drops every membership in org, as when it is deleted.
func (store *Store) ForgetOrganization(ctx context.Context, org string) error {
	_, err := store.database.Pool.Exec(ctx, `DELETE FROM agents_primary.edge_room_members WHERE organization_id = $1`, org)
	return err
}
