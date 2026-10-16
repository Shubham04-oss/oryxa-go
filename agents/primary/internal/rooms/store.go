package rooms

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/events"
	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

//go:embed migrations/*.sql
var migrations embed.FS

// migrationsAdvisoryLock keeps replicas that start together from installing
// this package's tables at once. Every agents/primary package picks its own
// number from the same 72635107xx range; the highest taken before this one
// was organizationagents' 7263510714.
const migrationsAdvisoryLock int64 = 7263510716

// ErrRoomNotFound is returned for a room whose teamspace does not exist. A
// room is a teamspace (see rooms.go's package doc), so every write here
// proves that by deriving organization_id from agents_primary.teamspaces
// rather than trusting a caller-supplied value nothing else validates.
var ErrRoomNotFound = errors.New("room not found")

// PostgresStore is the PostgreSQL-backed [Store]. It is not named Store,
// unlike its sibling packages' own stores, because rooms.go already spends
// that name on the interface this type implements.
type PostgresStore struct {
	database *postgreskit.Database
	// recordPosted records a stored message's event in the transaction that
	// stored it. It is recordPosted, but for a test that makes it fail.
	recordPosted func(ctx context.Context, tx pgx.Tx, organizationID string, message Message) error
}

// NewStore returns the store over database.
func NewStore(database *postgreskit.Database) *PostgresStore {
	return &PostgresStore{database: database, recordPosted: recordPosted}
}

// Migrate installs this package's tables. Running it twice is harmless.
func (store *PostgresStore) Migrate(ctx context.Context) error {
	return store.database.Migrate(ctx, "rooms", migrations, "migrations/*.sql", migrationsAdvisoryLock)
}

const messageColumns = `id, room_id, seq, author_handle, author_kind, author_user_id,
	author_agent_id, body, addressed_to, in_reply_to_seq, created_at, kind, data`

// Append implements [Store]. It allocates the room's next sequence number
// and inserts message under it, atomically, then returns the stored row.
//
// The sequence lives in its own counter row (room_message_seqs) rather than
// as a MAX(seq) over room_messages, because an increment folded into an
// INSERT is safe under concurrency in a way a read-then-write can never
// quite be: two Appends for the same room both running
// INSERT ... ON CONFLICT DO UPDATE SET next_seq = next_seq + 1 do not both
// compute "current max plus one" from the same snapshot and collide. The
// second one's statement blocks, as part of resolving the conflict, until
// the first commits, and only then increments what the first actually left
// behind. That is what makes the sequence gapless and unique without a
// retry loop on either side — the room_messages_room_idx UNIQUE(room_id,seq)
// constraint is a safety net behind this, not a substitute for it.
//
// The same statement derives organization_id from the room's teamspace (a
// room IS a teamspace) and doubles as the room's existence check: the
// SELECT ... FROM teamspaces supplies the one candidate row the upsert
// needs, so a room whose teamspace is gone yields no candidate, no upsert,
// and ErrRoomNotFound — on the first message exactly as on the thousandth.
//
// Every message stored is announced, in the same transaction, as
// agents.teamspace.room.message.posted.v1 in the agents outbox: the event
// commits with the message, and never without it.
func (store *PostgresStore) Append(ctx context.Context, message Message) (Message, error) {
	var stored Message
	err := pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		var err error
		stored, err = store.append(ctx, tx, message)
		return err
	})
	if errors.Is(err, ErrRoomNotFound) {
		return Message{}, ErrRoomNotFound
	}
	if err != nil {
		return Message{}, fmt.Errorf("append room message: %w", err)
	}
	return stored, nil
}

// append stores message in tx, as Append describes. Each of allocated runs
// once the message has its number and before it is inserted, while the
// room's counter row is locked; an error from one stops the append, and the
// caller's rollback gives the number back.
func (store *PostgresStore) append(ctx context.Context, tx pgx.Tx, message Message, allocated ...func(organizationID string) error) (Message, error) {
	// The store gives a message both of the things that make it one: its
	// place in the room, and its name. A caller that invented ids would have
	// to be trusted to keep them unique across every room at once, and the
	// one that forgot would collide here rather than where the mistake was.
	if strings.TrimSpace(message.ID) == "" {
		id, err := newMessageID()
		if err != nil {
			return Message{}, err
		}
		message.ID = id
	}
	message.CreatedAt = time.Now().UTC()
	if message.AddressedTo == nil {
		message.AddressedTo = []string{}
	}
	if message.Kind == "" {
		message.Kind = KindMessage
	}
	var organizationID string
	err := tx.QueryRow(ctx, `
		INSERT INTO agents_primary.room_message_seqs (room_id, organization_id, next_seq)
		SELECT $1, teamspace.organization_id, 2
		FROM agents_primary.teamspaces teamspace WHERE teamspace.id = $1
		ON CONFLICT (room_id) DO UPDATE SET next_seq = room_message_seqs.next_seq + 1
		RETURNING next_seq - 1, organization_id`,
		message.RoomID).Scan(&message.Seq, &organizationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Message{}, ErrRoomNotFound
	}
	if err != nil {
		return Message{}, fmt.Errorf("allocate room message sequence: %w", err)
	}
	for _, check := range allocated {
		if err := check(organizationID); err != nil {
			return Message{}, err
		}
	}
	if err := scanMessage(tx.QueryRow(ctx, `
		INSERT INTO agents_primary.room_messages (`+messageColumns+`, organization_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		RETURNING `+messageColumns,
		message.ID, message.RoomID, message.Seq, message.AuthorHandle, message.AuthorKind,
		message.AuthorUserID, message.AuthorAgentID, message.Body, message.AddressedTo,
		message.InReplyToSeq, message.CreatedAt, message.Kind, nullJSON(message.Data), organizationID), &message); err != nil {
		return Message{}, err
	}
	return message, store.recordPosted(ctx, tx, organizationID, message)
}

// errNotesPosted rolls back an AppendNotes whose call already has notes in
// the room, and with it the sequence number it took.
var errNotesPosted = errors.New("this call's notes are already in the room")

// AppendNotes implements [Store]. The teamspace is read in the organization
// it is said to be in, with its organization agent, which is who the notes
// are by: an agent that speaks for nobody, so the message has no author
// user. A paused teamspace's room takes no notes, as it takes no messages.
//
// Whether the call already has notes is asked after the room's sequence
// number is allocated, while the counter row is locked: two deliveries of
// the same notes queue there, and the second sees the first's committed
// message, gives its number back and answers with that message instead.
func (store *PostgresStore) AppendNotes(ctx context.Context, organizationID, roomID string, notes ParleyNotes, body string) (Message, bool, error) {
	data, err := json.Marshal(notes)
	if err != nil {
		return Message{}, false, fmt.Errorf("encode parley notes: %w", err)
	}
	var stored, existing Message
	err = pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		var status, agentID, agentName string
		err := tx.QueryRow(ctx, `SELECT teamspace.status, agent.id, agent.name
			FROM agents_primary.teamspaces teamspace
			JOIN agents_primary.organization_agents agent ON agent.id = teamspace.organization_agent_id
			WHERE teamspace.organization_id = $1 AND teamspace.id = $2`, organizationID, roomID).
			Scan(&status, &agentID, &agentName)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRoomNotFound
		}
		if err != nil {
			return fmt.Errorf("read the room's teamspace: %w", err)
		}
		if status != "active" {
			return ErrPaused
		}
		stored, err = store.append(ctx, tx, Message{
			RoomID: roomID, Kind: KindParleyNotes, Data: data,
			AuthorHandle: handle(agentName, map[string]bool{}), AuthorKind: SeatAgent, AuthorAgentID: agentID,
			Body: body,
		}, func(string) error {
			err := scanMessage(tx.QueryRow(ctx, `SELECT `+messageColumns+` FROM agents_primary.room_messages
				WHERE room_id = $1 AND kind = 'parley_notes' AND data->>'call_id' = $2`, roomID, notes.CallID), &existing)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			return errNotesPosted
		})
		return err
	})
	switch {
	case errors.Is(err, errNotesPosted):
		return existing, false, nil
	case errors.Is(err, ErrRoomNotFound), errors.Is(err, ErrPaused):
		return Message{}, false, err
	case err != nil:
		return Message{}, false, fmt.Errorf("append parley notes: %w", err)
	}
	return stored, true, nil
}

// nullJSON is data as a nullable jsonb column takes it: null for none.
func nullJSON(data json.RawMessage) any {
	if len(data) == 0 {
		return nil
	}
	return []byte(data)
}

// recordPosted records in tx that message, as stored, was posted in its
// room: who wrote it, its place, and whom it named, never what it said.
func recordPosted(ctx context.Context, tx pgx.Tx, organizationID string, message Message) error {
	event, err := events.NewEvent(agentsv1.TeamspaceRoomMessagePostedDetailType, posted(organizationID, message), message.CreatedAt)
	if err != nil {
		return err
	}
	trace := observability.CaptureTraceCarrier(ctx).Encode()
	if _, err := tx.Exec(ctx, `INSERT INTO agents_primary.agents_outbox
		(id, detail_type, detail, occurred_at, trace_context) VALUES ($1,$2,$3,$4,NULLIF($5,''))`,
		event.ID, event.DetailType, event.Data, event.OccurredAt, trace); err != nil {
		return fmt.Errorf("record the room message's event: %w", err)
	}
	return nil
}

// posted is the bus's view of a stored message. The room is the teamspace,
// so its id is the teamspace's. Whom it named is its AddressedTo, which the
// relay narrowed to the room's own seats before storing it.
func posted(organizationID string, message Message) agentsv1.TeamspaceRoomMessagePosted {
	author := agentsv1.TeamspaceRoomAuthor{Kind: agentsv1.TeamspaceRoomAuthorMember, UserID: message.AuthorUserID}
	if message.AuthorKind == SeatAgent {
		author = agentsv1.TeamspaceRoomAuthor{Kind: agentsv1.TeamspaceRoomAuthorAgent, UserID: message.AuthorUserID, AgentID: message.AuthorAgentID}
	}
	var addressed []string
	if len(message.AddressedTo) > 0 {
		addressed = slices.Clone(message.AddressedTo)
	}
	return agentsv1.TeamspaceRoomMessagePosted{
		OrganizationID: organizationID, TeamspaceID: message.RoomID,
		Message:          agentsv1.TeamspaceRoomMessage{ID: message.ID, Seq: message.Seq, InReplyToSeq: message.InReplyToSeq},
		Author:           author,
		AddressedHandles: addressed,
		PostedAt:         message.CreatedAt.UTC(),
	}
}

// Messages implements [Store].
func (store *PostgresStore) Messages(ctx context.Context, roomID string, afterSeq int64, limit int) ([]Message, error) {
	return store.listMessages(ctx, `SELECT `+messageColumns+` FROM agents_primary.room_messages
		WHERE room_id=$1 AND seq>$2 ORDER BY seq ASC LIMIT $3`, roomID, afterSeq, clampLimit(limit))
}

// Recent implements [Store]. The inner query takes the newest limit rows by
// seq; the outer one puts them back in reading order.
func (store *PostgresStore) Recent(ctx context.Context, roomID string, limit int) ([]Message, error) {
	return store.listMessages(ctx, `SELECT `+messageColumns+` FROM (
		SELECT `+messageColumns+` FROM agents_primary.room_messages
		WHERE room_id=$1 ORDER BY seq DESC LIMIT $2
	) recent ORDER BY seq ASC`, roomID, clampLimit(limit))
}

func (store *PostgresStore) listMessages(ctx context.Context, query string, args ...any) ([]Message, error) {
	rows, err := store.database.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list room messages: %w", err)
	}
	defer rows.Close()
	messages := []Message{}
	for rows.Next() {
		var message Message
		if err := scanMessage(rows, &message); err != nil {
			return nil, fmt.Errorf("scan room message: %w", err)
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

type scanner interface{ Scan(...any) error }

func scanMessage(row scanner, message *Message) error {
	var data []byte
	if err := row.Scan(&message.ID, &message.RoomID, &message.Seq, &message.AuthorHandle, &message.AuthorKind,
		&message.AuthorUserID, &message.AuthorAgentID, &message.Body, &message.AddressedTo,
		&message.InReplyToSeq, &message.CreatedAt, &message.Kind, &data); err != nil {
		return err
	}
	message.Data = nil
	if len(data) > 0 {
		message.Data = json.RawMessage(data)
	}
	if message.AddressedTo == nil {
		message.AddressedTo = []string{}
	}
	return nil
}

const briefColumns = `room_id, revision, through_seq, summary, decided, open_items, owed, updated_at`

// Brief implements [Store].
func (store *PostgresStore) Brief(ctx context.Context, roomID string) (Brief, error) {
	var brief Brief
	err := scanBrief(store.database.Pool.QueryRow(ctx, `SELECT `+briefColumns+`
		FROM agents_primary.room_briefs WHERE room_id=$1`, roomID), &brief)
	if errors.Is(err, pgx.ErrNoRows) {
		return zeroBrief(roomID), nil
	}
	if err != nil {
		return Brief{}, fmt.Errorf("get room brief: %w", err)
	}
	return brief, nil
}

// SaveBrief implements [Store]: it compares expected against the revision
// already stored and only writes when they match, so two compactions racing
// on the same room cannot clobber one another. The loser gets back what the
// winner just committed, unchanged, with no error.
//
// A room with no brief yet has no row for FOR UPDATE to lock, so the first
// statement idempotently plants one at revision 0 — deriving organization_id
// from the room's teamspace, same as Append, and failing the same way with
// ErrRoomNotFound when it does not exist — before the second statement locks
// whatever is there. From that point on, ordinary row locking does the
// rest: two concurrent SaveBriefs for the same room serialize on that lock,
// and whichever runs second sees the first's committed revision and fails
// the comparison cleanly instead of racing it.
func (store *PostgresStore) SaveBrief(ctx context.Context, brief Brief, expected int64) (Brief, error) {
	var result Brief
	err := pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO agents_primary.room_briefs
				(room_id, organization_id, revision, through_seq, summary, decided, open_items, owed, updated_at)
			SELECT $1, teamspace.organization_id, 0, 0, '', '[]'::jsonb, '[]'::jsonb, '[]'::jsonb, now()
			FROM agents_primary.teamspaces teamspace WHERE teamspace.id = $1
			ON CONFLICT (room_id) DO NOTHING`, brief.RoomID); err != nil {
			return fmt.Errorf("plant room brief: %w", err)
		}
		var current Brief
		err := scanBrief(tx.QueryRow(ctx, `SELECT `+briefColumns+`
			FROM agents_primary.room_briefs WHERE room_id=$1 FOR UPDATE`, brief.RoomID), &current)
		if errors.Is(err, pgx.ErrNoRows) {
			// The plant above inserted nothing, which only happens when the
			// room's teamspace does not exist: nothing to compact against.
			return ErrRoomNotFound
		}
		if err != nil {
			return err
		}
		if current.Revision != expected {
			result = current
			return nil
		}
		decided, err := json.Marshal(nonNilStrings(brief.Decided))
		if err != nil {
			return err
		}
		open, err := json.Marshal(nonNilStrings(brief.Open))
		if err != nil {
			return err
		}
		owed, err := json.Marshal(nonNilStrings(brief.Owed))
		if err != nil {
			return err
		}
		return scanBrief(tx.QueryRow(ctx, `UPDATE agents_primary.room_briefs SET
			revision=revision+1, through_seq=$2, summary=$3, decided=$4, open_items=$5, owed=$6, updated_at=$7
			WHERE room_id=$1 RETURNING `+briefColumns,
			current.RoomID, brief.ThroughSeq, brief.Summary, decided, open, owed, time.Now().UTC()), &result)
	})
	if errors.Is(err, ErrRoomNotFound) {
		return Brief{}, ErrRoomNotFound
	}
	if err != nil {
		return Brief{}, fmt.Errorf("save room brief: %w", err)
	}
	return result, nil
}

func zeroBrief(roomID string) Brief {
	return Brief{RoomID: roomID, Decided: []string{}, Open: []string{}, Owed: []string{}}
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func scanBrief(row scanner, brief *Brief) error {
	var decided, open, owed []byte
	if err := row.Scan(&brief.RoomID, &brief.Revision, &brief.ThroughSeq, &brief.Summary,
		&decided, &open, &owed, &brief.UpdatedAt); err != nil {
		return err
	}
	if err := json.Unmarshal(decided, &brief.Decided); err != nil {
		return fmt.Errorf("decode brief decided: %w", err)
	}
	if err := json.Unmarshal(open, &brief.Open); err != nil {
		return fmt.Errorf("decode brief open: %w", err)
	}
	if err := json.Unmarshal(owed, &brief.Owed); err != nil {
		return fmt.Errorf("decode brief owed: %w", err)
	}
	if brief.Decided == nil {
		brief.Decided = []string{}
	}
	if brief.Open == nil {
		brief.Open = []string{}
	}
	if brief.Owed == nil {
		brief.Owed = []string{}
	}
	return nil
}

// Watch implements [Store]. Presence only moves forward: GREATEST keeps a
// delayed duplicate ping from undoing a later one that already landed.
func (store *PostgresStore) Watch(ctx context.Context, roomID, userID string, at time.Time) error {
	tag, err := store.database.Pool.Exec(ctx, `
		INSERT INTO agents_primary.room_presence (room_id, organization_id, user_id, last_read_at)
		SELECT $1, teamspace.organization_id, $2, $3
		FROM agents_primary.teamspaces teamspace WHERE teamspace.id = $1
		ON CONFLICT (room_id, user_id) DO UPDATE
			SET last_read_at = GREATEST(room_presence.last_read_at, EXCLUDED.last_read_at)`,
		roomID, userID, at)
	if err != nil {
		return fmt.Errorf("watch room: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrRoomNotFound
	}
	return nil
}

// Watching implements [Store].
func (store *PostgresStore) Watching(ctx context.Context, roomID string, since time.Time) ([]string, error) {
	rows, err := store.database.Pool.Query(ctx, `
		SELECT user_id FROM agents_primary.room_presence
		WHERE room_id=$1 AND last_read_at>=$2
		ORDER BY user_id`, roomID, since)
	if err != nil {
		return nil, fmt.Errorf("list room watchers: %w", err)
	}
	defer rows.Close()
	userIDs := []string{}
	for rows.Next() {
		var userID string
		if err := rows.Scan(&userID); err != nil {
			return nil, fmt.Errorf("scan room watcher: %w", err)
		}
		userIDs = append(userIDs, userID)
	}
	return userIDs, rows.Err()
}

// MarkRead implements [Store]. The cursor only moves forward, as presence
// does, so a slow client's older read cannot undo a newer one; and it stops at
// the room's last message, so a seq from the future cannot hide what is said
// next.
func (store *PostgresStore) MarkRead(ctx context.Context, roomID, userID string, seq int64) (int64, error) {
	var read int64
	err := store.database.Pool.QueryRow(ctx, `
		INSERT INTO agents_primary.room_read_cursors (room_id, organization_id, user_id, last_read_seq, updated_at)
		SELECT $1, teamspace.organization_id, $2, LEAST($3, COALESCE((
			SELECT counter.next_seq - 1 FROM agents_primary.room_message_seqs counter WHERE counter.room_id = $1
		), 0)), now()
		FROM agents_primary.teamspaces teamspace WHERE teamspace.id = $1
		ON CONFLICT (room_id, user_id) DO UPDATE
			SET last_read_seq = GREATEST(room_read_cursors.last_read_seq, EXCLUDED.last_read_seq),
				updated_at = EXCLUDED.updated_at
		RETURNING last_read_seq`,
		roomID, userID, seq).Scan(&read)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrRoomNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("mark room read: %w", err)
	}
	return read, nil
}

// Activity implements [Store] in one statement, however many rooms it is
// asked about: each room's unread count and newest message are read beside
// one another, rather than a query per room.
func (store *PostgresStore) Activity(ctx context.Context, organizationID, userID string, roomIDs []string) (map[string]Activity, error) {
	activity := make(map[string]Activity, len(roomIDs))
	if len(roomIDs) == 0 {
		return activity, nil
	}
	rows, err := store.database.Pool.Query(ctx, `
		SELECT room.id, COALESCE(reader.last_read_seq, 0),
			(SELECT count(*) FROM agents_primary.room_messages unread
				WHERE unread.room_id = room.id AND unread.organization_id = $1
				  AND unread.seq > COALESCE(reader.last_read_seq, 0)
				  AND NOT (unread.author_kind = 'person' AND unread.author_user_id = $2)),
			newest.id, newest.room_id, newest.seq, newest.author_handle, newest.author_kind, newest.author_user_id,
			newest.author_agent_id, newest.body, newest.addressed_to, newest.in_reply_to_seq, newest.created_at,
			newest.kind
		FROM unnest($3::text[]) AS room(id)
		LEFT JOIN agents_primary.room_read_cursors reader ON reader.room_id = room.id AND reader.user_id = $2
		LEFT JOIN LATERAL (
			SELECT `+messageColumns+` FROM agents_primary.room_messages
			WHERE room_id = room.id AND organization_id = $1 ORDER BY seq DESC LIMIT 1
		) newest ON true`, organizationID, userID, roomIDs)
	if err != nil {
		return nil, fmt.Errorf("read room activity: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			roomID  string
			current Activity
			last    struct {
				id, roomID, handle, kind, userID, agentID, body, messageKind *string
				seq, inReplyTo                                               *int64
				addressed                                                    []string
				createdAt                                                    *time.Time
			}
		)
		if err := rows.Scan(&roomID, &current.LastReadSeq, &current.Unread,
			&last.id, &last.roomID, &last.seq, &last.handle, &last.kind, &last.userID,
			&last.agentID, &last.body, &last.addressed, &last.inReplyTo, &last.createdAt, &last.messageKind); err != nil {
			return nil, fmt.Errorf("scan room activity: %w", err)
		}
		if last.id != nil {
			current.Last = &Message{
				ID: *last.id, RoomID: *last.roomID, Seq: *last.seq, AuthorHandle: *last.handle,
				AuthorKind: SeatKind(*last.kind), AuthorUserID: *last.userID, AuthorAgentID: *last.agentID,
				Body: *last.body, AddressedTo: nonNilStrings(last.addressed), InReplyToSeq: *last.inReplyTo,
				CreatedAt: *last.createdAt, Kind: MessageKind(*last.messageKind),
			}
		}
		activity[roomID] = current
	}
	return activity, rows.Err()
}

// Thread implements [Threads] (declared in speaker.go): the empty string
// when agentID has not spoken in roomID yet.
func (store *PostgresStore) Thread(ctx context.Context, roomID, agentID string) (string, error) {
	var threadID string
	err := store.database.Pool.QueryRow(ctx, `
		SELECT thread_id FROM agents_primary.room_agent_threads
		WHERE room_id=$1 AND agent_id=$2`, roomID, agentID).Scan(&threadID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read room agent thread: %w", err)
	}
	return threadID, nil
}

// SetThread implements [Threads]. Last writer wins: two turns of the same
// agent in one room are already serialized by the relay that runs them, so
// there is no compare-and-set here the way there is for the brief. An empty
// threadID is a deliberate clear, not a no-op, so a caller telling us the
// old thread is gone is recorded exactly like a caller telling us a new one.
func (store *PostgresStore) SetThread(ctx context.Context, roomID, agentID, threadID string) error {
	tag, err := store.database.Pool.Exec(ctx, `
		INSERT INTO agents_primary.room_agent_threads (room_id, organization_id, agent_id, thread_id, updated_at)
		SELECT $1, teamspace.organization_id, $2, $3, now()
		FROM agents_primary.teamspaces teamspace WHERE teamspace.id = $1
		ON CONFLICT (room_id, agent_id) DO UPDATE
			SET thread_id = EXCLUDED.thread_id, updated_at = EXCLUDED.updated_at`,
		roomID, agentID, threadID)
	if err != nil {
		return fmt.Errorf("record room agent thread: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrRoomNotFound
	}
	return nil
}

func clampLimit(limit int) int {
	switch {
	case limit <= 0:
		return 100
	case limit > 500:
		return 500
	default:
		return limit
	}
}

// Forget implements [Store]: it removes a member's own messages, presence,
// read cursors and agent-thread pointers from organizationID's rooms, or,
// when userID is empty, everything every room of that organization holds. A
// message's owning member is its AuthorUserID, whether a person or their own
// agent said it, so scoping by it catches both. Parley notes are the
// organization agent's and have no author user, so only the organization's
// own Forget removes them; a member's Forget leaves the notes but takes
// their id off every action they owned. The brief is a room's own synthesis rather than
// any one member's, so a per-member Forget leaves it standing — the same
// convention teamspaces.Forget follows for a team-owned space when one
// contributor leaves it. A member's agent threads are found through
// member_agents' ownership, the same join organizationagents.Forget uses to
// reach a member's own follow-ups.
func (store *PostgresStore) Forget(ctx context.Context, organizationID, userID string) error {
	if strings.TrimSpace(userID) == "" {
		return pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `DELETE FROM agents_primary.room_messages WHERE organization_id=$1`, organizationID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM agents_primary.room_briefs WHERE organization_id=$1`, organizationID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM agents_primary.room_presence WHERE organization_id=$1`, organizationID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM agents_primary.room_agent_threads WHERE organization_id=$1`, organizationID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM agents_primary.room_read_cursors WHERE organization_id=$1`, organizationID); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `DELETE FROM agents_primary.room_message_seqs WHERE organization_id=$1`, organizationID)
			return err
		})
	}
	return pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM agents_primary.room_messages
			WHERE organization_id=$1 AND author_user_id=$2`, organizationID, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE agents_primary.room_messages
			SET data = jsonb_set(data, '{actions}', (
				SELECT COALESCE(jsonb_agg(CASE WHEN action->>'owner_user_id' = $2 THEN action - 'owner_user_id' ELSE action END), '[]'::jsonb)
				FROM jsonb_array_elements(data->'actions') AS action
			))
			WHERE organization_id=$1 AND kind='parley_notes'
				AND data->'actions' @> jsonb_build_array(jsonb_build_object('owner_user_id', $2::text))`, organizationID, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM agents_primary.room_presence
			WHERE organization_id=$1 AND user_id=$2`, organizationID, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM agents_primary.room_read_cursors
			WHERE organization_id=$1 AND user_id=$2`, organizationID, userID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM agents_primary.room_agent_threads
			WHERE organization_id=$1 AND agent_id IN (
				SELECT id FROM agents_primary.member_agents WHERE organization_id=$1 AND owner_user_id=$2
			)`, organizationID, userID)
		return err
	})
}

var (
	_ Store   = (*PostgresStore)(nil)
	_ Threads = (*PostgresStore)(nil)
)

// newMessageID names one message, the way every other id in this service is
// made.
func newMessageID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("create room message id: %w", err)
	}
	return "rmg_" + base64.RawURLEncoding.EncodeToString(bytes), nil
}
