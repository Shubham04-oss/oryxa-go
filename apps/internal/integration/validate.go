package integration

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// ErrInvalidManifest is what every *ValidationError is, for errors.Is.
var ErrInvalidManifest = errors.New("invalid manifest")

// Problem is one rule a manifest breaks, and where.
type Problem struct {
	// Path is where, as the manifest's JSON spells it:
	// auth.methods[0].fields[1].name.
	Path string
	// Rule is what the value there must be. It never quotes the value.
	Rule string
}

// ValidationError is every rule a manifest breaks. It names each by its path
// and never quotes a value, so it is safe to log and to show.
type ValidationError struct {
	Problems []Problem
}

func (e *ValidationError) Error() string {
	var message strings.Builder
	message.WriteString("invalid manifest: ")
	for i, problem := range e.Problems {
		if i > 0 {
			message.WriteString("; ")
		}
		message.WriteString(problem.Path + ": " + problem.Rule)
	}
	return message.String()
}

func (e *ValidationError) Unwrap() error { return ErrInvalidManifest }

var (
	// slugPattern is a toolkit, as apps' domain.CheckToolkit spells one, or an
	// OAuth client registration.
	slugPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	fieldNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	// upperSlugPattern is a tool or trigger, as apps' domain.CheckTool spells
	// a tool.
	upperSlugPattern  = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_]{0,127}$`)
	readerNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	// headerNamePattern is an HTTP field name: RFC 9110's token.
	headerNamePattern    = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
	parameterNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.~-]+$`)
	configNamePattern    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	placeholderPattern   = regexp.MustCompile(`\{[^{}]*\}`)
)

// kindRules is what a credential kind takes.
type kindRules struct {
	// standard are the kind's own fields, which the core holds and a
	// manifest never declares.
	standard []string
	oauth2   bool // takes an OAuth2 block, and needs one
	place    bool // takes a Place, and needs one
	// tokenResponse says the kind makes a token request, which discovered
	// fields and token_request uses need.
	tokenResponse bool
}

var kinds = map[string]kindRules{
	KindOAuth2Code:              {oauth2: true, tokenResponse: true},
	KindOAuth2ClientCredentials: {standard: []string{"client_id", "client_secret"}, oauth2: true, tokenResponse: true},
	KindAPIToken:                {standard: []string{"token"}, place: true},
	KindBasic:                   {standard: []string{"username", "password"}},
}

// revokeStyles says whether each style revokes at revoke_url, or needs none.
var revokeStyles = map[string]bool{
	RevokeRFC7009:         true,
	RevokeRFC7009JSON:     true,
	RevokeSlackAuthRevoke: true,
	RevokeNone:            false,
	RevokeGitHubToken:     false,
}

var formats = []string{FormatHostname, FormatHTTPSURL, FormatUUID, FormatEmail}

var hookScopes = []string{HookScopeConnection, HookScopeOrganization, HookScopeDeployment}

// schemeReads is what a verification scheme reads of a Verify.
type schemeReads struct {
	signature, encoding, signed, timestamp, token, issuer, audience, email bool
}

var schemes = map[string]schemeReads{
	VerifyHMACSHA256:  {signature: true, encoding: true, signed: true, timestamp: true},
	VerifyStaticToken: {token: true},
	VerifyJWTPush:     {issuer: true, audience: true, email: true},
}

// handshakes names the scheme each handshake belongs to: a signed one is
// answered after verification, an unsigned one only in its narrow window.
var handshakes = map[string]string{
	HandshakeSlackURLVerification: VerifyHMACSHA256,
	HandshakeGitHubPing:           VerifyHMACSHA256,
	HandshakeNotionVerification:   VerifyHMACSHA256,
	HandshakeGraphValidation:      VerifyStaticToken,
	HandshakeGoogleChannelSync:    VerifyStaticToken,
}

// reservedHeaders are headers no field places and no test call sends: the
// credential's own, and the ones net/http owns.
var reservedHeaders = []string{"authorization", "cookie", "host"}

// reservedAuthParams are the authorization request's own parameters, which
// auth_params never sets: overriding them would undo state, PKCE and the
// fixed redirect.
var reservedAuthParams = []string{
	"response_type", "client_id", "redirect_uri", "scope", "state", "code_challenge", "code_challenge_method",
}

// reservedTokenParams are the token request's own parameters, which a
// token_request use never sets.
var reservedTokenParams = []string{
	"grant_type", "code", "redirect_uri", "code_verifier", "client_id", "client_secret", "refresh_token", "scope",
}

// Validate checks the manifest against every rule the core relies on. It
// answers a *ValidationError naming each rule broken, or nil.
func (m Manifest) Validate() error {
	v := &validator{manifest: m}
	v.identity()
	v.auth()
	if m.Webhook != nil {
		v.webhook(*m.Webhook)
	}
	v.tools()
	v.readers()
	v.triggers()
	if len(v.problems) == 0 {
		return nil
	}
	return &ValidationError{Problems: v.problems}
}

type validator struct {
	manifest Manifest
	problems []Problem
}

// sharedURLs is what the URLs every method shares hold: api_base and the test
// path.
type sharedURLs struct {
	base, test []string
	// lead is the placeholder api_base begins with, if any.
	lead string
	// known is false when either URL is missing or could not be read. Which
	// placeholders they hold is then unknown, so no url field is blamed for
	// filling none.
	known bool
}

func (v *validator) fail(path, rule string) {
	v.problems = append(v.problems, Problem{Path: path, Rule: rule})
}

func (v *validator) identity() {
	if !slugPattern.MatchString(v.manifest.Toolkit) {
		v.fail("toolkit", "must be Composio's lowercase slug for the app, such as github")
	}
	if v.manifest.Version < 1 {
		v.fail("version", "must be 1 or more")
	}
	if strings.TrimSpace(v.manifest.Name) == "" {
		v.fail("name", "is required")
	}
}

// auth checks api_base, the test call and the methods, since every method
// must be able to fill api_base's and the test path's placeholders.
func (v *validator) auth() {
	var shared sharedURLs
	var baseKnown, testKnown bool
	shared.base, shared.lead, baseKnown = v.absoluteURL("api_base", v.manifest.APIBase, false)
	shared.test, testKnown = v.test(v.manifest.Auth.Test)
	shared.known = baseKnown && testKnown

	methods := v.manifest.Auth.Methods
	if len(methods) == 0 {
		v.fail("auth.methods", "needs at least one method")
		return
	}
	seen := map[string]bool{}
	for i, method := range methods {
		path := index("auth.methods", i)
		if seen[method.Kind] {
			v.fail(path+".kind", "repeats an earlier method's kind, and a connection picks its method by kind")
		}
		seen[method.Kind] = true
		v.method(path, method, shared)
	}
}

func (v *validator) method(path string, method Method, shared sharedURLs) {
	rules, known := kinds[method.Kind]
	if !known {
		v.fail(path+".kind", "must be a credential kind the core knows: oauth2_code, oauth2_client_credentials, api_token or basic")
	}

	fields := map[string]Field{}
	for j, field := range method.Fields {
		fieldPath := index(path+".fields", j)
		v.field(fieldPath, field, rules, known)
		if _, repeated := fields[field.Name]; repeated {
			v.fail(fieldPath+".name", "repeats an earlier field's name")
			continue
		}
		fields[field.Name] = field
	}

	if known {
		switch {
		case rules.oauth2 && method.OAuth2 == nil:
			v.fail(path+".oauth2", "is required for this kind")
		case !rules.oauth2 && method.OAuth2 != nil:
			v.fail(path+".oauth2", "is only for the oauth2 kinds")
		}
		switch {
		case rules.place && method.Place == nil:
			v.fail(path+".place", "is required for api_token")
		case !rules.place && method.Place != nil:
			v.fail(path+".place", "is only for api_token")
		}
	}
	var oauthNames []string
	oauthKnown := true
	if method.OAuth2 != nil {
		oauthNames, oauthKnown = v.oauth2(path+".oauth2", method.Kind, *method.OAuth2, fields)
	}
	if method.Place != nil {
		v.place(path+".place", *method.Place)
	}

	// Methods are alternatives, so api_base and the test path, which every
	// method shares, may only hold what each method can fill.
	for _, name := range shared.base {
		if !hasUse(fields[name], InURL) {
			v.fail("api_base", "holds a placeholder that names no url field of "+path)
		}
	}
	if field, declared := fields[shared.lead]; shared.lead != "" && declared && field.Validate.Format != FormatHTTPSURL {
		v.fail("api_base", "begins with a placeholder, so its field in "+path+" must have the https_url format")
	}
	for _, name := range shared.test {
		if !hasUse(fields[name], InURL) {
			v.fail("auth.test.path", "holds a placeholder that names no url field of "+path)
		}
	}

	if !shared.known || !oauthKnown {
		return
	}
	for j, field := range method.Fields {
		for k, use := range field.Use {
			if use.In == InURL && !slices.Contains(shared.base, field.Name) &&
				!slices.Contains(shared.test, field.Name) && !slices.Contains(oauthNames, field.Name) {
				v.fail(index(index(path+".fields", j)+".use", k), "fills a placeholder that none of the manifest's URLs hold")
			}
		}
	}
}

func (v *validator) field(path string, field Field, rules kindRules, known bool) {
	switch {
	case !fieldNamePattern.MatchString(field.Name):
		v.fail(path+".name", "must match ^[a-z][a-z0-9_]{0,31}$")
	case known && slices.Contains(rules.standard, field.Name):
		v.fail(path+".name", "is one of this kind's standard fields, which the core holds and a manifest never declares")
	}
	if strings.TrimSpace(field.Label) == "" {
		v.fail(path+".label", "is required")
	}
	switch field.Level {
	case LevelConnection, LevelClient:
		if field.Discover != "" {
			v.fail(path+".discover", "is only for a discovered field")
		}
	case LevelDiscovered:
		if !validPointer(field.Discover, false) {
			v.fail(path+".discover", "must be a JSON pointer (RFC 6901) into the token response")
		}
		if known && !rules.tokenResponse {
			v.fail(path+".level", "is discovered, but this kind has no token response to discover it in")
		}
	default:
		v.fail(path+".level", "must be connection, client or discovered")
	}
	v.validation(path+".validate", field.Validate)

	placesInURL := false
	for k, use := range field.Use {
		usePath := index(path+".use", k)
		switch use.In {
		case InHeader:
			switch {
			case !headerNamePattern.MatchString(use.Name):
				v.fail(usePath+".name", "must be an HTTP header name")
			case slices.Contains(reservedHeaders, strings.ToLower(use.Name)):
				v.fail(usePath+".name", "is Authorization, Cookie or Host, which only the core sets")
			}
		case InQuery:
			if !parameterNamePattern.MatchString(use.Name) {
				v.fail(usePath+".name", "must be a query parameter name")
			}
		case InTokenRequest:
			switch {
			case !parameterNamePattern.MatchString(use.Name):
				v.fail(usePath+".name", "must be a form parameter name")
			case slices.Contains(reservedTokenParams, use.Name):
				v.fail(usePath+".name", "is one of the token request's own parameters")
			}
			if known && !rules.tokenResponse {
				v.fail(usePath+".in", "is token_request, but this kind makes no token request")
			}
		case InURL:
			placesInURL = true
			if use.Name != "" {
				v.fail(usePath+".name", "must be empty: a url use fills the field's own {name} placeholder")
			}
			if field.Secret {
				v.fail(usePath+".in", "puts a secret in a URL, where logs and traces would see it")
			}
		default:
			v.fail(usePath+".in", "must be header, query, url or token_request")
		}
		if use.Template != "" {
			v.template(usePath+".template", use.Template, "value")
		}
	}
	if placesInURL && field.Validate.Format == "" && field.Validate.Pattern == "" && len(field.Validate.Enum) == 0 {
		v.fail(path+".validate", "needs a format, a pattern or an enum, since the field fills a URL")
	}
}

func (v *validator) validation(path string, validation Validation) {
	if validation.Format != "" && !slices.Contains(formats, validation.Format) {
		v.fail(path+".format", "must be hostname, https_url, uuid or email")
	}
	if _, err := validation.Regexp(); err != nil {
		// The compiler's message quotes the pattern, so it is not passed on.
		v.fail(path+".pattern", "must compile as an RE2 pattern, anchored")
	}
	seen := map[string]bool{}
	for i, value := range validation.Enum {
		switch {
		case value == "":
			v.fail(index(path+".enum", i), "must not be empty")
		case seen[value]:
			v.fail(index(path+".enum", i), "repeats an earlier value")
		}
		seen[value] = true
	}
	if validation.MaxLen < 0 {
		v.fail(path+".max_length", "must not be negative")
	}
}

// template checks a template that must hold {want} and nothing else in
// braces.
func (v *validator) template(path, template, want string) {
	names, ok := placeholders(template)
	switch {
	case !ok:
		v.fail(path, "has an unbalanced brace or an empty placeholder")
	case len(names) != 1 || names[0] != want:
		v.fail(path, "must hold {"+want+"} and no other placeholder")
	case strings.ContainsAny(template, "\r\n"):
		v.fail(path, "must be one line")
	}
}

func (v *validator) place(path string, place Place) {
	switch place.In {
	case InHeader:
		if !headerNamePattern.MatchString(place.Name) {
			v.fail(path+".name", "must be an HTTP header name")
		}
	case InQuery:
		if !parameterNamePattern.MatchString(place.Name) {
			v.fail(path+".name", "must be a query parameter name")
		}
	default:
		v.fail(path+".in", "must be header or query")
	}
	v.template(path+".template", place.Template, "token")
}

// oauth2 checks an OAuth2 block. It answers the placeholders its URLs hold,
// and false when a URL it needs is missing or could not be read.
func (v *validator) oauth2(path, kind string, oauth OAuth2, fields map[string]Field) ([]string, bool) {
	code := kind == KindOAuth2Code
	if (code || oauth.Client != "") && !slugPattern.MatchString(oauth.Client) {
		v.fail(path+".client", "must be a lowercase slug naming the OAuth client registration, such as google")
	}

	var names []string
	known := true
	checkURL := func(name, raw string, required, beforeToken bool) {
		urlPath := path + "." + name
		if raw == "" && !required {
			return
		}
		found, lead, read := v.absoluteURL(urlPath, raw, true)
		known = known && read
		for _, placeholder := range found {
			field, declared := fields[placeholder]
			switch {
			case !declared || !hasUse(field, InURL):
				v.fail(urlPath, "holds a placeholder that names no url field of this method")
			case beforeToken && field.Level == LevelDiscovered:
				v.fail(urlPath, "holds a discovered field, which is only known after the token request")
			}
		}
		if field, declared := fields[lead]; lead != "" && declared && field.Validate.Format != FormatHTTPSURL {
			v.fail(urlPath, "begins with a placeholder, so its field must have the https_url format")
		}
		names = append(names, found...)
	}
	checkURL("auth_url", oauth.AuthURL, code, true)
	checkURL("token_url", oauth.TokenURL, true, true)
	checkURL("revoke_url", oauth.RevokeURL, false, false)

	style := oauth.RevokeStyle
	if style == "" {
		style = RevokeRFC7009
	}
	takesURL, styleKnown := revokeStyles[style]
	switch {
	case !styleKnown:
		v.fail(path+".revoke_style", "must be a revoke style the core knows: rfc7009, rfc7009_json, none, github_token or slack_auth_revoke")
	case takesURL && oauth.RevokeURL == "":
		v.fail(path+".revoke_url", "is required by this revoke style; an app that cannot revoke says revoke_style none")
	case !takesURL && oauth.RevokeURL != "":
		v.fail(path+".revoke_url", "must be empty for this revoke style")
	}

	v.scopes(path+".scopes", oauth.Scopes)
	switch oauth.ScopeSep {
	case "", " ", ScopeSeparatorComma:
	default:
		v.fail(path+".scope_separator", "must be a space or a comma")
	}
	switch oauth.ClientAuth {
	case ClientAuthHeader, ClientAuthBody:
	default:
		v.fail(path+".client_auth", "must be header or body")
	}
	for _, key := range slices.Sorted(maps.Keys(oauth.AuthParams)) {
		if key == "" || slices.Contains(reservedAuthParams, key) {
			v.fail(path+".auth_params", "must not set an empty name or the authorization request's own parameters: "+
				strings.Join(reservedAuthParams, ", "))
			break
		}
	}
	return names, known
}

// scopes checks a list of scopes: each is one scope, listed once.
func (v *validator) scopes(path string, scopes []string) {
	seen := map[string]bool{}
	for i, scope := range scopes {
		switch {
		case scope == "" || strings.ContainsFunc(scope, unicode.IsSpace) || strings.Contains(scope, ScopeSeparatorComma):
			v.fail(index(path, i), "must be one scope, with no space or comma in it")
		case seen[scope]:
			v.fail(index(path, i), "repeats an earlier scope")
		}
		seen[scope] = true
	}
}

// test checks the test call. It answers the placeholders its path holds, and
// false when the path is missing or could not be read.
func (v *validator) test(test Test) ([]string, bool) {
	names, known := v.relativePath("auth.test.path", test.Path)
	switch {
	case test.Label == "":
		v.fail("auth.test.label", "is required: a JSON pointer to the account's name")
	case !validPointer(test.Label, false):
		v.fail("auth.test.label", "must be a JSON pointer (RFC 6901)")
	}
	if test.AccountRef != "" && !validPointer(test.AccountRef, false) {
		v.fail("auth.test.account_ref", "must be a JSON pointer (RFC 6901)")
	}
	for _, name := range slices.Sorted(maps.Keys(test.Headers)) {
		value := test.Headers[name]
		switch {
		case !headerNamePattern.MatchString(name) || slices.Contains(reservedHeaders, strings.ToLower(name)):
			v.fail("auth.test.headers", "must name HTTP headers other than Authorization, Cookie and Host")
		case value == "" || strings.ContainsAny(value, "{}\r\n"):
			v.fail("auth.test.headers", "must hold fixed, one-line values")
		}
	}
	return names, known
}

// absoluteURL checks one of the manifest's absolute URLs, which may hold
// {field} placeholders. It answers the placeholders, the one the URL begins
// with, if any, which stands for a whole https URL, and false when the URL is
// missing or its placeholders could not be read.
func (v *validator) absoluteURL(path, raw string, allowQuery bool) ([]string, string, bool) {
	if raw == "" {
		v.fail(path, "is required")
		return nil, "", false
	}
	names, ok := placeholders(raw)
	if !ok {
		v.fail(path, "has an unbalanced brace or an empty placeholder")
		return nil, "", false
	}
	lead, sample := "", raw
	if strings.HasPrefix(raw, "{") {
		lead = names[0]
		sample = "https://placeholder.invalid" + raw[len(lead)+2:]
	}
	sample = placeholderPattern.ReplaceAllString(sample, "x")
	parsed, err := url.Parse(sample)
	switch {
	case err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Opaque != "" ||
		parsed.User != nil || strings.Contains(sample, "#"):
		v.fail(path, "must be an absolute https URL, with no credentials or fragment")
	case !allowQuery && (parsed.RawQuery != "" || parsed.ForceQuery):
		v.fail(path, "must not carry a query")
	}
	return names, lead, true
}

// relativePath checks a path under api_base, which may carry a query. It
// answers the placeholders the path holds, and false when it is missing or
// its placeholders could not be read.
func (v *validator) relativePath(path, raw string) ([]string, bool) {
	if raw == "" {
		v.fail(path, "is required")
		return nil, false
	}
	names, ok := placeholders(raw)
	if !ok {
		v.fail(path, "has an unbalanced brace or an empty placeholder")
		return nil, false
	}
	if rule := pathRule(placeholderPattern.ReplaceAllString(raw, "x"), true); rule != "" {
		v.fail(path, rule)
	}
	return names, true
}

// readPost checks one of a read tool's POST paths: under api_base, with no
// query, and with {} only as a whole segment.
func (v *validator) readPost(path, raw string) {
	segments := strings.Split(raw, "/")
	for i, segment := range segments {
		switch {
		case segment == "{}":
			segments[i] = "x"
		case strings.ContainsAny(segment, "{}"):
			v.fail(path, "may hold {} only as a whole segment, standing for any one segment")
			return
		}
	}
	if rule := pathRule(strings.Join(segments, "/"), false); rule != "" {
		v.fail(path, rule)
	}
}

// pathRule answers the rule a path under api_base breaks, or "". A path that
// could leave api_base, by naming another host or climbing out, breaks one.
func pathRule(sample string, allowQuery bool) string {
	if !strings.HasPrefix(sample, "/") || strings.HasPrefix(sample, "//") {
		return "must be a path under api_base, beginning with a single /"
	}
	parsed, err := url.Parse("https://base.invalid" + sample)
	if err != nil || parsed.Host != "base.invalid" || strings.Contains(sample, "#") {
		return "must be a path under api_base, with no fragment"
	}
	for _, segment := range strings.Split(parsed.Path, "/") {
		if segment == "." || segment == ".." {
			return "must not climb out of api_base with dot segments"
		}
	}
	if !allowQuery && (parsed.RawQuery != "" || parsed.ForceQuery) {
		return "must not carry a query"
	}
	return ""
}

func (v *validator) webhook(hook Webhook) {
	if !slices.Contains(hookScopes, hook.Scope) {
		v.fail("webhook.scope", "must be connection, organization or deployment")
	}
	v.verify("webhook.verify", hook.Verify, hook.DeliveryID)
	v.locator("webhook.delivery_id", hook.DeliveryID, deliveryLocator)
	if hook.Handshake == "" {
		return
	}
	scheme, known := handshakes[hook.Handshake]
	_, typeKnown := schemes[hook.Verify.Type]
	switch {
	case !known:
		v.fail("webhook.handshake", "must be a handshake the core knows: slack_url_verification, github_ping, "+
			"graph_validation, google_channel_sync or notion_verification")
	case typeKnown && scheme != hook.Verify.Type:
		v.fail("webhook.handshake", "belongs to another verification scheme")
	}
}

func (v *validator) verify(path string, verify Verify, delivery Locator) {
	reads, known := schemes[verify.Type]
	if !known {
		v.fail(path+".type", "must be a verification scheme the core knows: hmac_sha256, static_token or jwt_push")
		return
	}
	unread := func(name string, set, read bool) {
		if set && !read {
			v.fail(path+"."+name, "is not read by this scheme")
		}
	}
	unread("signature", verify.Signature != nil, reads.signature)
	unread("encoding", verify.Encoding != "", reads.encoding)
	unread("signed", verify.Signed != "", reads.signed)
	unread("timestamp", verify.Timestamp != nil, reads.timestamp)
	unread("token", verify.Token != nil, reads.token)
	unread("issuer", verify.Issuer != "", reads.issuer)
	unread("audience", verify.Audience != "", reads.audience)
	unread("email", verify.Email != "", reads.email)
	switch {
	case verify.Tolerance < 0:
		v.fail(path+".tolerance_seconds", "must not be negative")
	case verify.Tolerance > 0 && verify.Timestamp == nil:
		v.fail(path+".tolerance_seconds", "is only for a scheme that reads a timestamp")
	}

	switch verify.Type {
	case VerifyHMACSHA256:
		v.hmac(path, verify, delivery)
	case VerifyStaticToken:
		if verify.Token == nil {
			v.fail(path+".token", "is required for static_token")
		} else {
			v.locator(path+".token", *verify.Token, tokenLocator)
		}
	case VerifyJWTPush:
		v.jwtPush(path, verify)
	}
}

func (v *validator) hmac(path string, verify Verify, delivery Locator) {
	if verify.Signature == nil {
		v.fail(path+".signature", "is required for hmac_sha256")
	} else {
		v.locator(path+".signature", *verify.Signature, signatureLocator)
	}
	if verify.Encoding != EncodingHex && verify.Encoding != EncodingBase64 {
		v.fail(path+".encoding", "must be hex or base64")
	}
	names, ok := placeholders(verify.Signed)
	signs := func(name string) bool { return slices.Contains(names, name) }
	switch {
	case verify.Signed == "":
		v.fail(path+".signed", "is required for hmac_sha256")
	case !ok:
		v.fail(path+".signed", "has an unbalanced brace or an empty placeholder")
	case slices.ContainsFunc(names, func(name string) bool { return name != "body" && name != "timestamp" && name != "id" }):
		v.fail(path+".signed", "may only hold {body}, {timestamp} and {id}")
	case !signs("body"):
		v.fail(path+".signed", "must hold {body}: a signature that leaves the body out proves nothing about it")
	case signs("timestamp") && verify.Timestamp == nil:
		v.fail(path+".signed", "holds {timestamp}, which needs a timestamp locator")
	case signs("id") && delivery.Header == "" && delivery.JSON == "":
		v.fail(path+".signed", "holds {id}, which needs a delivery_id read from a header or the body")
	}
	if verify.Timestamp != nil {
		v.locator(path+".timestamp", *verify.Timestamp, timestampLocator)
		// A timestamp in the body is signed with it; one in a header is only
		// signed if the template says so, and otherwise anyone could replay a
		// delivery with a fresh one.
		if verify.Timestamp.Header != "" && verify.Signed != "" && ok && !signs("timestamp") {
			v.fail(path+".timestamp", "is read from a header, so the signature must cover it ({timestamp})")
		}
	}
}

func (v *validator) jwtPush(path string, verify Verify) {
	issuer, err := url.Parse(verify.Issuer)
	switch {
	case verify.Issuer == "":
		v.fail(path+".issuer", "is required for jwt_push")
	case err != nil || issuer.Scheme != "https" || issuer.Host == "" || strings.ContainsAny(verify.Issuer, "{}"):
		v.fail(path+".issuer", "must be a fixed https URL")
	}
	names, ok := placeholders(verify.Audience)
	switch {
	case verify.Audience == "":
		v.fail(path+".audience", "is required for jwt_push")
	case !ok || slices.ContainsFunc(names, func(name string) bool { return name != "endpoint_url" }):
		v.fail(path+".audience", "may hold only the {endpoint_url} placeholder")
	}
	if verify.Email != "" && !emailValue(verify.Email) {
		v.fail(path+".email", "must be an email address, or one {config:name} placeholder")
	}
}

// emailValue says whether a jwt_push email is an address, or a whole
// {config:name} placeholder.
func emailValue(value string) bool {
	if strings.ContainsAny(value, "{}") {
		name, opened := strings.CutPrefix(value, "{config:")
		name, closed := strings.CutSuffix(name, "}")
		return opened && closed && configNamePattern.MatchString(name)
	}
	at := strings.IndexByte(value, '@')
	return at > 0 && at < len(value)-1 && strings.Count(value, "@") == 1 && !strings.ContainsFunc(value, unicode.IsSpace)
}

// locatorRole is what a Locator finds, which decides what it may use.
type locatorRole int

const (
	signatureLocator locatorRole = iota
	timestampLocator
	tokenLocator
	deliveryLocator
)

func (v *validator) locator(path string, locator Locator, role locatorRole) {
	sources := 0
	if locator.Header != "" {
		sources++
		if !headerNamePattern.MatchString(locator.Header) {
			v.fail(path+".header", "must be an HTTP header name")
		}
	}
	if locator.JSON != "" {
		sources++
		if !validPointer(locator.JSON, role == tokenLocator) {
			v.fail(path+".json", "must be a JSON pointer (RFC 6901) into the body; only a token locator may use *")
		}
	}
	if locator.Hash {
		sources++
		if role != deliveryLocator {
			v.fail(path+".hash", "is only for a delivery id")
		}
	}
	if sources != 1 {
		v.fail(path, "must read exactly one of header, json or hash")
	}
	if locator.Hash && locator.Prefix != "" {
		v.fail(path+".prefix", "is not read from a hash")
	}
	if locator.Split != "" && role != signatureLocator {
		v.fail(path+".split", "is only for a signature")
	}
	switch {
	case role == timestampLocator && locator.Unit != UnitSeconds && locator.Unit != UnitMilliseconds:
		v.fail(path+".unit", "must be s or ms")
	case role != timestampLocator && locator.Unit != "":
		v.fail(path+".unit", "is only for a timestamp")
	}
}

func (v *validator) tools() {
	prefix := strings.ToUpper(v.manifest.Toolkit) + "_"
	seen := map[string]bool{}
	for i, tool := range v.manifest.Tools {
		path := index("tools", i)
		switch {
		case !prefixed(tool.Name, prefix):
			v.fail(path+".name", "must be an uppercase slug beginning with the toolkit's own prefix, as GITHUB_LIST_ISSUES does")
		case seen[tool.Name]:
			v.fail(path+".name", "repeats an earlier tool's name")
		}
		seen[tool.Name] = true
		if strings.TrimSpace(tool.Description) == "" {
			v.fail(path+".description", "is required")
		}
		if tool.ReadOnly && tool.Destructive {
			v.fail(path+".destructive", "cannot be set on a read tool, which changes nothing")
		}
		if len(tool.ReadPOSTs) > 0 && !tool.ReadOnly {
			v.fail(path+".read_posts", "are only for read tools")
		}
		for j, post := range tool.ReadPOSTs {
			v.readPost(index(path+".read_posts", j), post)
		}
		v.scopes(path+".scopes", tool.Scopes)
		v.schema(path+".input_schema", tool.InputSchema)
	}
}

func (v *validator) readers() {
	seen := map[string]bool{}
	for i, reader := range v.manifest.Readers {
		path := index("readers", i)
		switch {
		case !readerNamePattern.MatchString(reader.Name):
			v.fail(path+".name", "must match ^[a-z][a-z0-9_]{0,63}$")
		case seen[reader.Name]:
			v.fail(path+".name", "repeats an earlier reader's name")
		}
		seen[reader.Name] = true
		if len(reader.Kinds) == 0 {
			v.fail(path+".kinds", "needs at least one kind")
		}
		kindsSeen := map[string]bool{}
		for j, kind := range reader.Kinds {
			switch {
			case strings.TrimSpace(kind) == "":
				v.fail(index(path+".kinds", j), "must not be empty")
			case kindsSeen[kind]:
				v.fail(index(path+".kinds", j), "repeats an earlier kind")
			}
			kindsSeen[kind] = true
		}
		for j, post := range reader.ReadPOSTs {
			v.readPost(index(path+".read_posts", j), post)
		}
		v.schema(path+".target_schema", reader.TargetSchema)
		v.scopes(path+".scopes", reader.Scopes)
	}
}

func (v *validator) triggers() {
	prefix := strings.ToUpper(v.manifest.Toolkit) + "_"
	seen := map[string]bool{}
	for i, trigger := range v.manifest.Triggers {
		path := index("triggers", i)
		switch {
		case !prefixed(trigger.Slug, prefix):
			v.fail(path+".slug", "must be an uppercase slug beginning with the toolkit's own prefix, as GITHUB_COMMIT_EVENT does")
		case seen[trigger.Slug]:
			v.fail(path+".slug", "repeats an earlier trigger's slug")
		}
		seen[trigger.Slug] = true
		if strings.TrimSpace(trigger.Name) == "" {
			v.fail(path+".name", "is required")
		}
		switch trigger.Kind {
		case TriggerPoll:
		case TriggerPush:
			if v.manifest.Webhook == nil {
				v.fail(path+".kind", "is push, which needs the manifest's webhook")
			}
		default:
			v.fail(path+".kind", "must be poll or push")
		}
		v.schema(path+".config_schema", trigger.ConfigSchema)
		v.schema(path+".payload_schema", trigger.PayloadSchema)
		if len(trigger.Sample) > 0 && !json.Valid(trigger.Sample) {
			v.fail(path+".sample", "must be valid JSON")
		}
		v.scopes(path+".scopes", trigger.Scopes)
	}
}

// schema checks a JSON Schema: present, valid JSON, and an object.
func (v *validator) schema(path string, schema json.RawMessage) {
	var object map[string]json.RawMessage
	switch {
	case len(bytes.TrimSpace(schema)) == 0:
		v.fail(path, "is required")
	case !json.Valid(schema) || json.Unmarshal(schema, &object) != nil || object == nil:
		v.fail(path, "must be a JSON Schema: valid JSON, and an object")
	}
}

// prefixed says whether a tool or trigger is an uppercase slug beginning with
// its toolkit's prefix, as Composio names them.
func prefixed(name, prefix string) bool {
	return upperSlugPattern.MatchString(name) && strings.HasPrefix(name, prefix) && len(name) > len(prefix)
}

// validPointer says whether pointer is an RFC 6901 JSON pointer to something
// inside a document, rather than the whole of it. With wildcard, a "*" token
// stands for every element of an array.
func validPointer(pointer string, wildcard bool) bool {
	if !strings.HasPrefix(pointer, "/") {
		return false
	}
	for _, token := range strings.Split(pointer[1:], "/") {
		if token == "*" && !wildcard {
			return false
		}
		for i := 0; i < len(token); i++ {
			if token[i] != '~' {
				continue
			}
			if i+1 == len(token) || (token[i+1] != '0' && token[i+1] != '1') {
				return false
			}
			i++
		}
	}
	return true
}

// placeholders answers the {name} placeholders a template holds, each once,
// in order. It answers false for an unbalanced brace or an empty name.
func placeholders(template string) ([]string, bool) {
	var names []string
	for rest := template; rest != ""; {
		open := strings.IndexAny(rest, "{}")
		if open < 0 {
			break
		}
		if rest[open] == '}' {
			return nil, false
		}
		length := strings.IndexAny(rest[open+1:], "{}")
		if length <= 0 || rest[open+1+length] == '{' {
			return nil, false
		}
		if name := rest[open+1 : open+1+length]; !slices.Contains(names, name) {
			names = append(names, name)
		}
		rest = rest[open+1+length+1:]
	}
	return names, true
}

func hasUse(field Field, in string) bool {
	return slices.ContainsFunc(field.Use, func(use Use) bool { return use.In == in })
}

func index(path string, i int) string { return fmt.Sprintf("%s[%d]", path, i) }
