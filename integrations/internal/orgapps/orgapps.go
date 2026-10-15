// Package orgapps keeps the OAuth and key-based apps connections are made
// through: an org's own ("bring your own"), and Oryxa's, which the service
// loads from its configuration (design §9.1 to §9.3). The rule it enforces:
// one live row per (org, app); a replaced row is retired, not deleted, so the
// connections pinned to it keep refreshing and revoking through it.
//
// Ported from apps/internal/signin/clients.go.
package orgapps

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/vault"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

var (
	// ErrInvalid means the input is not acceptable; the error says which field.
	ErrInvalid = errors.New("orgapps: invalid input")
	// ErrNotFound means the org has no live app of its own for the app.
	ErrNotFound = errors.New("orgapps: no such app")
	// ErrConflict means the live app changed while it was being saved.
	ErrConflict = errors.New("orgapps: the app was replaced or removed meanwhile")
	// ErrNotSetUp means neither the org nor Oryxa has an app for the app.
	ErrNotSetUp = errors.New("orgapps: no app is set up")
	// ErrBroken means the app chosen failed its health check; it blocks new connects.
	ErrBroken = errors.New("orgapps: the app is broken")
)

// kinds lists the apps that connect natively, and the kind of app each uses.
var kinds = map[string]string{
	"github":    "github_app",
	"slack":     "slack_app",
	"notion":    "api_key",
	"google":    "oauth_client",
	"microsoft": "oauth_client",
}

const (
	maxOrgLength    = 128
	maxClientID     = 512
	maxSecretLength = 16 << 10
	maxExtraKeys    = 16
	maxExtraValue   = 512
	tableOrgApps    = "org_apps"
)

var extraKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// Input is an org saving its own app, or Oryxa's own app being loaded.
type Input struct {
	// ClientID is the app's public id. An api_key app has none.
	ClientID string
	// Secret is write-only. A new app needs one; saving the live app again
	// with none keeps its secret.
	Secret secrets.Secret
	// WebhookSecret is the secret the app signs its webhooks with. Left out,
	// it keeps what is saved.
	WebhookSecret secrets.Secret
	// Extra is what is not secret: a GitHub App id, a tenant id. A key left
	// out keeps its value; an empty value clears it.
	Extra     map[string]string
	ExpiresAt *time.Time
	ActorID   string
}

// View is an app as an admin may see it: never a secret, only a hint.
type View struct {
	ID       string `json:"id"`
	OrgID    string `json:"org_id,omitempty"`
	App      string `json:"app"`
	Kind     string `json:"kind"`
	ClientID string `json:"client_id,omitempty"`
	// SecretSet says a secret is saved; SecretHint is its last four characters
	// when it is 16 characters or longer.
	SecretSet         bool              `json:"secret_set"`
	SecretHint        string            `json:"secret_hint,omitempty"`
	WebhookSecretSet  bool              `json:"webhook_secret_set"`
	WebhookSecretHint string            `json:"webhook_secret_hint,omitempty"`
	Extra             map[string]string `json:"extra,omitempty"`
	ExpiresAt         *time.Time        `json:"expires_at,omitempty"`
	Status            string            `json:"status"`
	CreatedBy         string            `json:"created_by,omitempty"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
}

// Chosen is the app a new connection is made through, and how.
type Chosen struct {
	ID       string
	OrgID    string
	App      string
	Kind     string
	ClientID string
	// Mode is the connection's mode: "byo" for the org's own app, "oryxa" for ours.
	Mode string
}

// Service saves and chooses apps.
type Service struct {
	pool   *pgxpool.Pool
	sealer vault.Sealer
	now    func() time.Time
}

// New returns a Service over pool, sealing secrets with sealer.
func New(pool *pgxpool.Pool, sealer vault.Sealer) *Service {
	return &Service{pool: pool, sealer: sealer, now: time.Now}
}

// row is an org_apps row.
type row struct {
	ID            string
	OrgID         string
	App           string
	Kind          string
	ClientID      string
	Secret        *secrets.SealedData
	WebhookSecret *secrets.SealedData
	Extra         map[string]string
	ExpiresAt     *time.Time
	Status        string
	CreatedBy     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

const columns = `id, coalesce(org_id, ''), app, kind, client_id, secret, webhook_secret, extra, expires_at, status, created_by, created_at, updated_at`

func scan(r pgx.Row) (row, error) {
	var out row
	err := r.Scan(&out.ID, &out.OrgID, &out.App, &out.Kind, &out.ClientID, &out.Secret, &out.WebhookSecret,
		&out.Extra, &out.ExpiresAt, &out.Status, &out.CreatedBy, &out.CreatedAt, &out.UpdatedAt)
	return out, err
}

// orgArg is the org_id value: NULL for Oryxa's own app.
func orgArg(org string) *string {
	if org == "" {
		return nil
	}
	return &org
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// live reads the live row of (org, app), locking it when lock is set.
func live(ctx context.Context, q querier, org, app string, lock bool) (row, error) {
	query := `SELECT ` + columns + ` FROM integrations.org_apps
		WHERE org_id IS NOT DISTINCT FROM $1 AND app = $2 AND status <> 'retired'`
	if lock {
		query += ` FOR UPDATE`
	}
	got, err := scan(q.QueryRow(ctx, query, orgArg(org), app))
	if errors.Is(err, pgx.ErrNoRows) {
		return row{}, ErrNotFound
	}
	return got, err
}

// Put saves the org's own app. The same client id as the live one updates it
// in place; another makes a new live row and retires the old one. It answers
// whether anything changed.
func (s *Service) Put(ctx context.Context, org, app string, in Input) (View, bool, error) {
	if err := checkOrg(org); err != nil {
		return View{}, false, err
	}
	return s.put(ctx, org, app, in)
}

// Load saves Oryxa's own app (org_id NULL) from the service's configuration.
func (s *Service) Load(ctx context.Context, app string, in Input) (View, bool, error) {
	return s.put(ctx, "", app, in)
}

func (s *Service) put(ctx context.Context, org, app string, in Input) (View, bool, error) {
	kind, ok := kinds[app]
	if !ok {
		return View{}, false, invalid("app", "is not an app that connects natively")
	}
	in.ClientID = strings.TrimSpace(in.ClientID)
	secret := secrets.Secret(strings.TrimSpace(in.Secret.Reveal()))
	webhook := secrets.Secret(strings.TrimSpace(in.WebhookSecret.Reveal()))
	if err := checkInput(kind, in, secret, webhook, s.now()); err != nil {
		return View{}, false, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return View{}, false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a rollback after commit is a no-op

	now := s.now().UTC().Truncate(time.Microsecond) // what Postgres keeps
	current, err := live(ctx, tx, org, app, true)
	switch {
	case err == nil && current.ClientID == in.ClientID:
		next, changed, err := s.update(ctx, current, in, secret, webhook, now)
		if err != nil {
			return View{}, false, err
		}
		if changed {
			if err := write(ctx, tx, next); err != nil {
				return View{}, false, err
			}
		}
		view, err := s.view(ctx, next)
		if err != nil {
			return View{}, false, err
		}
		return view, changed, tx.Commit(ctx)
	case err != nil && !errors.Is(err, ErrNotFound):
		return View{}, false, err
	}

	if secret == "" {
		return View{}, false, invalid("secret", "is required for a new app")
	}
	id, err := newID()
	if err != nil {
		return View{}, false, err
	}
	next := row{ID: id, OrgID: org, App: app, Kind: kind, ClientID: in.ClientID, Extra: cleanExtra(nil, in.Extra),
		ExpiresAt: in.ExpiresAt, Status: "ok", CreatedBy: in.ActorID, CreatedAt: now, UpdatedAt: now}
	if next.Secret, err = s.seal(ctx, next, "secret", secret); err != nil {
		return View{}, false, err
	}
	if next.WebhookSecret, err = s.seal(ctx, next, "webhook_secret", webhook); err != nil {
		return View{}, false, err
	}
	if current.ID != "" {
		if _, err := tx.Exec(ctx, `UPDATE integrations.org_apps SET status = 'retired', updated_at = $2 WHERE id = $1`, current.ID, now); err != nil {
			return View{}, false, err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO integrations.org_apps (id, org_id, app, kind, client_id, secret, webhook_secret, extra, expires_at, status, created_by, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		next.ID, orgArg(org), next.App, next.Kind, next.ClientID, next.Secret, next.WebhookSecret, next.Extra,
		next.ExpiresAt, next.Status, next.CreatedBy, next.CreatedAt, next.UpdatedAt); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return View{}, false, ErrConflict
		}
		return View{}, false, err
	}
	view, err := s.view(ctx, next)
	if err != nil {
		return View{}, false, err
	}
	return view, true, tx.Commit(ctx)
}

// update applies in over the live row with the same client id.
func (s *Service) update(ctx context.Context, current row, in Input, secret, webhook secrets.Secret, now time.Time) (row, bool, error) {
	next := current
	changed := false
	for _, field := range []struct {
		name   string
		value  secrets.Secret
		target **secrets.SealedData
	}{{"secret", secret, &next.Secret}, {"webhook_secret", webhook, &next.WebhookSecret}} {
		if field.value == "" {
			continue
		}
		if *field.target != nil {
			existing, err := s.sealer.Open(ctx, where(current, field.name), **field.target)
			if err != nil {
				return row{}, false, err
			}
			if subtle.ConstantTimeCompare([]byte(existing.Reveal()), []byte(field.value.Reveal())) == 1 {
				continue
			}
		}
		sealed, err := s.seal(ctx, current, field.name, field.value)
		if err != nil {
			return row{}, false, err
		}
		*field.target = sealed
		changed = true
	}
	if extra := cleanExtra(current.Extra, in.Extra); !maps.Equal(extra, current.Extra) {
		next.Extra, changed = extra, true
	}
	if in.ExpiresAt != nil && (current.ExpiresAt == nil || !current.ExpiresAt.Equal(*in.ExpiresAt)) {
		next.ExpiresAt, changed = in.ExpiresAt, true
	}
	if changed {
		next.UpdatedAt = now
	}
	return next, changed, nil
}

func write(ctx context.Context, tx pgx.Tx, r row) error {
	tag, err := tx.Exec(ctx, `UPDATE integrations.org_apps SET secret = $2, webhook_secret = $3, extra = $4, expires_at = $5, updated_at = $6
		WHERE id = $1 AND status <> 'retired'`, r.ID, r.Secret, r.WebhookSecret, r.Extra, r.ExpiresAt, r.UpdatedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

// Get answers the org's live app.
func (s *Service) Get(ctx context.Context, org, app string) (View, error) {
	if err := checkOrg(org); err != nil {
		return View{}, err
	}
	r, err := live(ctx, s.pool, org, app, false)
	if err != nil {
		return View{}, err
	}
	return s.view(ctx, r)
}

// Retire retires the org's live app. Connections made through it keep
// refreshing and revoking through it. It answers false when none was live.
func (s *Service) Retire(ctx context.Context, org, app string) (bool, error) {
	if err := checkOrg(org); err != nil {
		return false, err
	}
	tag, err := s.pool.Exec(ctx, `UPDATE integrations.org_apps SET status = 'retired', updated_at = $3
		WHERE org_id = $1 AND app = $2 AND status <> 'retired'`, org, app, s.now().UTC())
	return tag.RowsAffected() > 0, err
}

// Choose answers the app a new connection for org goes through (design
// §9.1): the org's live app, else Oryxa's, else ErrNotSetUp. A broken app
// blocks the connect rather than falling through to Oryxa's.
func (s *Service) Choose(ctx context.Context, org, app string) (Chosen, error) {
	if err := checkOrg(org); err != nil {
		return Chosen{}, err
	}
	for _, candidate := range []struct{ org, mode string }{{org, "byo"}, {"", "oryxa"}} {
		r, err := live(ctx, s.pool, candidate.org, app, false)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return Chosen{}, err
		}
		if r.Status == "broken" {
			return Chosen{}, fmt.Errorf("%w: %s", ErrBroken, app)
		}
		return Chosen{ID: r.ID, OrgID: r.OrgID, App: r.App, Kind: r.Kind, ClientID: r.ClientID, Mode: candidate.mode}, nil
	}
	return Chosen{}, fmt.Errorf("%w: %s", ErrNotSetUp, app)
}

func (s *Service) view(ctx context.Context, r row) (View, error) {
	v := View{ID: r.ID, OrgID: r.OrgID, App: r.App, Kind: r.Kind, ClientID: r.ClientID, Extra: maps.Clone(r.Extra),
		ExpiresAt: r.ExpiresAt, Status: r.Status, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
	for _, field := range []struct {
		name   string
		sealed *secrets.SealedData
		set    *bool
		hint   *string
	}{{"secret", r.Secret, &v.SecretSet, &v.SecretHint}, {"webhook_secret", r.WebhookSecret, &v.WebhookSecretSet, &v.WebhookSecretHint}} {
		if field.sealed == nil {
			continue
		}
		opened, err := s.sealer.Open(ctx, where(r, field.name), *field.sealed)
		if err != nil {
			return View{}, err
		}
		*field.set, *field.hint = true, hint(opened)
	}
	return v, nil
}

func (s *Service) seal(ctx context.Context, r row, field string, value secrets.Secret) (*secrets.SealedData, error) {
	if value == "" {
		return nil, nil
	}
	sealed, err := s.sealer.Seal(ctx, where(r, field), []byte(value.Reveal()))
	if err != nil {
		return nil, err
	}
	return &sealed, nil
}

func where(r row, field string) vault.Where {
	return vault.Where{Table: tableOrgApps, Org: r.OrgID, Row: r.ID + "/" + field}
}

// hint is what may be shown of a secret: its last four characters, when it
// is long enough that they give nothing away.
func hint(secret secrets.Secret) string {
	value := []rune(secret.Reveal())
	if len(value) < 16 {
		return ""
	}
	return string(value[len(value)-4:])
}

func checkOrg(org string) error {
	if org == "" || len(org) > maxOrgLength || strings.ContainsFunc(org, func(r rune) bool {
		return r == '/' || unicode.IsSpace(r) || unicode.IsControl(r)
	}) {
		return invalid("org", "must be one word without '/' of at most 128 characters")
	}
	return nil
}

func checkInput(kind string, in Input, secret, webhook secrets.Secret, now time.Time) error {
	switch {
	case kind == "api_key" && in.ClientID != "":
		return invalid("client_id", "is not taken by a key-based app")
	case kind != "api_key" && in.ClientID == "":
		return invalid("client_id", "is required")
	case len(in.ClientID) > maxClientID || strings.ContainsFunc(in.ClientID, unicode.IsSpace):
		return invalid("client_id", "must be one word of at most 512 characters")
	case len(secret) > maxSecretLength:
		return invalid("secret", "is too long")
	case len(webhook) > maxSecretLength:
		return invalid("webhook_secret", "is too long")
	case in.ExpiresAt != nil && !in.ExpiresAt.After(now):
		return invalid("expires_at", "must be in the future")
	case len(in.Extra) > maxExtraKeys:
		return invalid("extra", "takes at most 16 keys")
	}
	for key, value := range in.Extra {
		if !extraKey.MatchString(key) {
			return invalid("extra", "holds a key that is not lowercase letters, digits and '_'")
		}
		if len(value) > maxExtraValue {
			return invalid("extra."+key, "is too long")
		}
	}
	return nil
}

// cleanExtra merges typed over current: a key left out keeps its value, an
// empty value clears it.
func cleanExtra(current, typed map[string]string) map[string]string {
	out := maps.Clone(current)
	if out == nil {
		out = map[string]string{}
	}
	for key, value := range typed {
		if value = strings.TrimSpace(value); value == "" {
			delete(out, key)
		} else {
			out[key] = value
		}
	}
	return out
}

func invalid(field, rule string) error {
	return fmt.Errorf("%w: %s %s", ErrInvalid, field, rule)
}

func newID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "oa_" + hex.EncodeToString(b[:]), nil
}
