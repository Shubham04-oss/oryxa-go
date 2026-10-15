package manifest

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

var (
	idRE        = regexp.MustCompile(`^[a-z][a-z0-9_]{1,31}$`)
	nameRE      = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	toolRE      = regexp.MustCompile(`^([a-z][a-z0-9_]{1,31})\.([a-z][a-z0-9_]{0,63})$`)
	hostRE      = regexp.MustCompile(`^(\*\.)?[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)
	headerRE    = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	scheduleRE  = regexp.MustCompile(`^rate\(([1-9][0-9]*) (minutes?|hours?|days?)\)$`)
	lookbackRE  = regexp.MustCompile(`^PT(?:[1-9][0-9]*H)?(?:[1-9][0-9]*M)?(?:[1-9][0-9]*S)?$`)
	errCodeRE   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	placeholder = regexp.MustCompile(`\{[^{}]*\}`)
)

var (
	levels      = []string{"personal", "org"}
	modes       = []string{"oryxa", "byo", "user_key"}
	effects     = []string{"read", "write", "external", "delete"}
	syncModes   = []string{"full", "incremental", "webhook"}
	deleteModes = []string{"feed", "field", "webhook", "reconcile"}
	limitScopes = []string{"app", "connection", "user"}
	schemes     = []string{"hmac_sha256_github", "slack_v0", "jwt", "channel_token", "client_state"}
)

// Validate checks the manifest on its own: shapes, names, URLs, schemas and
// the references between its parts. The error lists every problem. What needs
// the cassettes (effects against real calls, description lint, the tool
// budget) is the connector gate's job.
func (m *Manifest) Validate() error {
	v := &validator{m: m}
	v.manifest()
	if len(v.errs) == 0 {
		return nil
	}
	return fmt.Errorf("manifest %q is invalid: %w", m.ID, errors.Join(v.errs...))
}

type validator struct {
	m    *Manifest
	errs []error
}

func (v *validator) errf(path, format string, args ...any) {
	v.errs = append(v.errs, fmt.Errorf("%s: %s", path, fmt.Sprintf(format, args...)))
}

func (v *validator) manifest() {
	m := v.m
	if !idRE.MatchString(m.ID) {
		v.errf("id", "%q must match %s", m.ID, idRE)
	}
	if m.Version < 1 {
		v.errf("version", "must be 1 or more")
	}
	if strings.TrimSpace(m.DisplayName) == "" {
		v.errf("display_name", "is required")
	}
	v.enumList("connection_levels", m.ConnectionLevels, levels)
	v.auth()
	v.schema("config", m.Config, false)
	v.baseURL()
	v.allowlist()
	v.verify()
	v.rateLimit()
	v.tools()
	v.streams()
	v.webhooks()
	for i, r := range m.ReauthOn {
		if (r.Status != 0) == (r.Code != "") || (r.Status != 0 && (r.Status < 100 || r.Status > 599)) ||
			(r.Code != "" && !errCodeRE.MatchString(r.Code)) {
			v.errf(fmt.Sprintf("reauth_on[%d]", i), "must be an HTTP status or a lowercase error code")
		}
	}
	if len(m.Tools) == 0 && len(m.Streams) == 0 {
		v.errf("tools", "a connector needs at least one tool or stream")
	}
}

func (v *validator) auth() {
	a := v.m.Auth
	v.enumList("auth.credential_modes", a.CredentialModes, modes)
	for _, mode := range a.CredentialModes {
		switch mode {
		case "oryxa", "byo":
			if a.OAuth2 == nil && a.GitHubApp == nil {
				v.errf("auth.credential_modes", "%q needs auth.oauth2 or auth.github_app", mode)
			}
		case "user_key":
			if a.UserKey == nil {
				v.errf("auth.credential_modes", "%q needs auth.user_key", mode)
			}
		}
	}
	if o := a.OAuth2; o != nil {
		v.oauth2(o)
	}
	if k := a.UserKey; k != nil {
		v.schema("auth.user_key.fields", k.Fields, true)
		if k.BaseURL != "" {
			v.allowedURL("auth.user_key.base_url", k.BaseURL)
		}
		if !headerRE.MatchString(k.Inject.Header) {
			v.errf("auth.user_key.inject.header", "%q is not a header name", k.Inject.Header)
		}
		if k.Inject.Value == "" {
			v.errf("auth.user_key.inject.value", "is required")
		}
	}
	if g := a.GitHubApp; g != nil {
		if g.ManifestTemplate == "" || strings.ContainsAny(g.ManifestTemplate, `/\`) ||
			strings.Contains(g.ManifestTemplate, "..") || !strings.HasSuffix(g.ManifestTemplate, ".json") {
			v.errf("auth.github_app.manifest_template", "must be a .json file name in the connector's folder")
		}
	}
}

func (v *validator) oauth2(o *OAuth2) {
	v.httpsURL("auth.oauth2.authorization_url", o.AuthorizationURL)
	v.httpsURL("auth.oauth2.token_url", o.TokenURL)
	if o.RevokeURL != "" {
		v.httpsURL("auth.oauth2.revoke_url", o.RevokeURL)
	}
	if len(o.TokenKinds) == 0 {
		v.errf("auth.oauth2.token_kinds", "needs at least one token kind")
	}
	seen := map[string]bool{}
	for i, k := range o.TokenKinds {
		path := fmt.Sprintf("auth.oauth2.token_kinds[%d]", i)
		if !nameRE.MatchString(k.Name) {
			v.errf(path+".name", "%q must match %s", k.Name, nameRE)
		}
		if seen[k.Name] {
			v.errf(path+".name", "%q is declared twice", k.Name)
		}
		seen[k.Name] = true
		if k.TokenPointer != "" && !validPointer(k.TokenPointer) {
			v.errf(path+".token_pointer", "%q is not a JSON pointer", k.TokenPointer)
		}
	}
	if r := o.Refresh; r != nil && (r.AheadSeconds < 0 || r.ReuseWindowS < 0) {
		v.errf("auth.oauth2.refresh", "seconds cannot be negative")
	}
	names := map[string]bool{}
	for i, out := range o.Outputs {
		path := fmt.Sprintf("auth.oauth2.outputs[%d]", i)
		if !nameRE.MatchString(out.Name) {
			v.errf(path+".name", "%q must match %s", out.Name, nameRE)
		}
		if names[out.Name] {
			v.errf(path+".name", "%q is declared twice", out.Name)
		}
		names[out.Name] = true
		if !validPointer(out.Pointer) {
			v.errf(path+".pointer", "%q is not a JSON pointer", out.Pointer)
		}
		if out.Request != "" {
			v.httpsURL(path+".request", out.Request)
		}
		if s := out.Select; s != nil && (!validPointer(s.Pointer) || s.Equals == "") {
			v.errf(path+".select", "needs a JSON pointer and a value to equal")
		}
	}
}

// baseURL requires https and a host the allowlist covers, so a tool can't be
// pointed at a host the egress proxy would refuse.
func (v *validator) baseURL() { v.allowedURL("base_url", v.m.BaseURL) }

// allowedURL is httpsURL plus the allowlist check. Placeholders (Jira's cloud
// id, its site) stand for config values and outputs; the host is checked with
// each replaced by one label, so a placeholder can't widen the allowlist.
func (v *validator) allowedURL(path, raw string) {
	u := v.httpsURL(path, placeholder.ReplaceAllString(raw, "x"))
	if u == nil {
		return
	}
	if !v.m.AllowsHost(u.Hostname()) {
		v.errf(path, "host %q is not in egress_allowlist", u.Hostname())
	}
}

// AllowsHost reports whether host is on the egress allowlist: listed exactly,
// or under a "*." entry (one label or more, never the bare domain).
func (m *Manifest) AllowsHost(host string) bool {
	return slices.ContainsFunc(m.EgressAllowlist, func(pattern string) bool { return hostAllowed(pattern, host) })
}

func hostAllowed(pattern, host string) bool {
	if suffix, ok := strings.CutPrefix(pattern, "*"); ok {
		return strings.HasSuffix(host, suffix) && len(host) > len(suffix)
	}
	return pattern == host
}

func (v *validator) allowlist() {
	if len(v.m.EgressAllowlist) == 0 {
		v.errf("egress_allowlist", "needs at least one host")
	}
	seen := map[string]bool{}
	for i, h := range v.m.EgressAllowlist {
		path := fmt.Sprintf("egress_allowlist[%d]", i)
		if !hostRE.MatchString(h) {
			v.errf(path, "%q must be a lowercase host name, optionally with a leading \"*.\"", h)
		} else if _, err := netip.ParseAddr(h); err == nil {
			v.errf(path, "%q is an IP address", h)
		}
		if seen[h] {
			v.errf(path, "%q is listed twice", h)
		}
		seen[h] = true
	}
}

func (v *validator) verify() {
	ver := v.m.Verify
	if ver == nil {
		return
	}
	if !strings.HasPrefix(ver.Path, "/") {
		v.errf("verify.path", "must start with /")
	}
	v.tokenKind("verify.token_kind", ver.TokenKind)
}

func (v *validator) rateLimit() {
	r := v.m.RateLimit
	if r == nil {
		return
	}
	if r.Scope != "" && !slices.Contains(limitScopes, r.Scope) {
		v.errf("rate_limit.scope", "%q must be one of %s", r.Scope, strings.Join(limitScopes, ", "))
	}
	if r.PerHour < 0 || r.MaxConcurrency < 0 {
		v.errf("rate_limit", "per_hour and max_concurrency cannot be negative")
	}
	for name, h := range map[string]string{"remaining": r.Headers.Remaining, "reset": r.Headers.Reset, "retry_after": r.Headers.RetryAfter} {
		if h != "" && !headerRE.MatchString(h) {
			v.errf("rate_limit.headers."+name, "%q is not a header name", h)
		}
	}
}

func (v *validator) tools() {
	seen := map[string]bool{}
	for i, t := range v.m.Tools {
		path := fmt.Sprintf("tools[%d]", i)
		match := toolRE.FindStringSubmatch(t.Name)
		switch {
		case match == nil:
			v.errf(path+".name", "%q must be <app>.<action>, lowercase", t.Name)
		case match[1] != v.m.ID:
			v.errf(path+".name", "%q must start with %q", t.Name, v.m.ID+".")
		}
		if seen[t.Name] {
			v.errf(path+".name", "%q is declared twice", t.Name)
		}
		seen[t.Name] = true
		if strings.TrimSpace(t.Description) == "" {
			v.errf(path+".description", "is required")
		}
		if !slices.Contains(effects, t.Effect) {
			v.errf(path+".effect", "%q must be one of %s", t.Effect, strings.Join(effects, ", "))
		}
		v.schema(path+".input_schema", t.InputSchema, true)
		v.schema(path+".output_schema", t.OutputSchema, true)
		v.tokenKind(path+".token_kind", t.TokenKind)
	}
}

func (v *validator) streams() {
	seen := map[string]bool{}
	for i, s := range v.m.Streams {
		path := fmt.Sprintf("streams[%d]", i)
		if !nameRE.MatchString(s.Name) {
			v.errf(path+".name", "%q must match %s", s.Name, nameRE)
		}
		if seen[s.Name] {
			v.errf(path+".name", "%q is declared twice", s.Name)
		}
		seen[s.Name] = true
		v.enumList(path+".modes", s.Modes, syncModes)
		if s.Schedule != "" && !scheduleRE.MatchString(s.Schedule) {
			v.errf(path+".schedule", "%q must look like rate(15 minutes)", s.Schedule)
		}
		if s.Deletes != "" && !slices.Contains(deleteModes, s.Deletes) {
			v.errf(path+".deletes", "%q must be one of %s", s.Deletes, strings.Join(deleteModes, ", "))
		}
		if s.Lookback != "" {
			if !lookbackRE.MatchString(s.Lookback) || s.Lookback == "PT" {
				v.errf(path+".lookback", "%q must be an ISO 8601 time duration such as PT5M", s.Lookback)
			}
			if !slices.Contains(s.Modes, "incremental") {
				v.errf(path+".lookback", "only an incremental stream has a lookback")
			}
		}
		if s.Document == nil && s.ACL == "" {
			v.errf(path, "needs a document mapping or an acl source")
		}
		if d := s.Document; d != nil {
			for field, p := range map[string]string{"title": d.Title, "body": d.Body, "url": d.URL, "author": d.Author, "updated_at": d.UpdatedAt} {
				if (field == "title" || p != "") && !validPointer(p) {
					v.errf(path+".document."+field, "%q is not a JSON pointer", p)
				}
			}
		}
		if slices.Contains(s.Modes, "webhook") && v.m.Webhooks == nil {
			v.errf(path+".modes", "webhook mode needs a webhooks section")
		}
		if s.Deletes == "webhook" && !v.deletedByWebhook(s.Name) {
			v.errf(path+".deletes", "webhook deletes need a webhooks.events entry for this stream with delete_on")
		}
	}
}

func (v *validator) deletedByWebhook(stream string) bool {
	if v.m.Webhooks == nil {
		return false
	}
	for _, e := range v.m.Webhooks.Events {
		if e.Stream == stream && len(e.DeleteOn) > 0 {
			return true
		}
	}
	return false
}

func (v *validator) webhooks() {
	w := v.m.Webhooks
	if w == nil {
		return
	}
	if !slices.Contains(schemes, w.Verification.Scheme) {
		v.errf("webhooks.verification.scheme", "%q must be one of %s", w.Verification.Scheme, strings.Join(schemes, ", "))
	}
	if (w.Verification.Scheme == "jwt" || w.Verification.Scheme == "channel_token") && !headerRE.MatchString(w.Verification.Header) {
		v.errf("webhooks.verification.header", "the %s scheme needs a header name", w.Verification.Scheme)
	}
	if w.Routing != "org_app" {
		v.errf("webhooks.routing", "%q must be \"org_app\"", w.Routing)
	}
	if len(w.RouteBy) == 0 && w.RouteHeader == "" {
		v.errf("webhooks.route_by", "needs a JSON pointer or route_header to find the connection")
	}
	for i, p := range w.RouteBy {
		if !validPointer(p) {
			v.errf(fmt.Sprintf("webhooks.route_by[%d]", i), "%q is not a JSON pointer", p)
		}
	}
	for name, h := range map[string]string{"route_header": w.RouteHeader, "delivery_id_header": w.DeliveryIDHeader} {
		if h != "" && !headerRE.MatchString(h) {
			v.errf("webhooks."+name, "%q is not a header name", h)
		}
	}
	if w.ReplayWindowS < 0 || w.FallbackPollS < 0 {
		v.errf("webhooks", "replay_window_s and fallback_poll_s cannot be negative")
	}
	if s := w.Subscription; s != nil && (s.MaxTTLS < 1 || s.RenewAt <= 0 || s.RenewAt >= 1) {
		v.errf("webhooks.subscription", "needs max_ttl_s of 1 or more and renew_at between 0 and 1")
	}
	streams := map[string]bool{}
	for _, s := range v.m.Streams {
		streams[s.Name] = true
	}
	for name, e := range w.Events {
		path := "webhooks.events[" + name + "]"
		if name == "" {
			v.errf(path, "the event name is empty")
		}
		if e.Stream == "" && e.Trigger == "" {
			v.errf(path, "needs a stream to re-fetch or a trigger")
		}
		if e.Stream != "" && !streams[e.Stream] {
			v.errf(path+".stream", "%q is not a stream of this connector", e.Stream)
		}
		if e.Trigger != "" && !nameRE.MatchString(e.Trigger) {
			v.errf(path+".trigger", "%q must match %s", e.Trigger, nameRE)
		}
		if e.ID != "" && !validPointer(e.ID) {
			v.errf(path+".id", "%q is not a JSON pointer", e.ID)
		}
		if e.Stream != "" && e.ID == "" {
			v.errf(path+".id", "a stream re-fetch needs the pointer to the object id")
		}
		if len(e.DeleteOn) > 0 && e.Stream == "" {
			v.errf(path+".delete_on", "only an event for a stream can delete")
		}
	}
}

// tokenKind checks a reference against the OAuth token kinds. Without OAuth
// (key-based apps) there is nothing to check it against.
func (v *validator) tokenKind(path, kind string) {
	o := v.m.Auth.OAuth2
	if kind == "" || o == nil {
		return
	}
	if !slices.ContainsFunc(o.TokenKinds, func(k TokenKind) bool { return k.Name == kind }) {
		v.errf(path, "%q is not a token kind in auth.oauth2.token_kinds", kind)
	}
}

func (v *validator) enumList(path string, got, allowed []string) {
	if len(got) == 0 {
		v.errf(path, "needs at least one of %s", strings.Join(allowed, ", "))
	}
	for i, g := range got {
		if !slices.Contains(allowed, g) {
			v.errf(path, "%q must be one of %s", g, strings.Join(allowed, ", "))
		}
		if slices.Index(got, g) != i {
			v.errf(path, "%q is listed twice", g)
		}
	}
}

// httpsURL reports a problem unless raw is an https URL with a host, and returns it.
func (v *validator) httpsURL(path, raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		v.errf(path, "%q must be an https URL", raw)
		return nil
	}
	return u
}

// schema checks that raw decodes as a JSON Schema and, with object set, that
// it describes an object. Patterns are not compiled: oryxa-platform's schemas
// use ECMA 262 patterns (\u escapes, {1,2048}) that Go's regexp refuses.
// ponytail: a pattern Go can't compile is only caught when the host validates
// a call against it (#291).
func (v *validator) schema(path string, raw json.RawMessage, object bool) {
	if len(raw) == 0 {
		if object {
			v.errf(path, "is required")
		}
		return
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(raw, &s); err != nil {
		v.errf(path, "is not a JSON Schema: %v", err)
		return
	}
	if object && s.Type != "object" {
		v.errf(path, `must have "type": "object"`)
	}
}

// validPointer reports whether p is a non-root RFC 6901 JSON pointer.
func validPointer(p string) bool {
	if !strings.HasPrefix(p, "/") {
		return false
	}
	for i := 0; i < len(p); i++ {
		if p[i] == '~' && (i+1 >= len(p) || (p[i+1] != '0' && p[i+1] != '1')) {
			return false
		}
	}
	return true
}
