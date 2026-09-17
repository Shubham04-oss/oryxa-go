# Provider API Research (2026): Atlassian Cloud & HubSpot

Target use case: a **Go service** that (a) registers webhooks via REST and (b) calls REST
"actions" with an **OAuth2 access token**.

Research method: official OpenAPI specifications, official developer documentation pages,
and official Atlassian Knowledge Base articles, fetched directly. Where a claim could not
be confirmed from an official source it is explicitly marked **[UNCONFIRMED]**.

> Note on Atlassian doc quality: `developer.atlassian.com` is a JS-rendered site whose
> prose sometimes lags the machine-readable OpenAPI spec. Where the prose and the spec
> disagree, this report treats **the OpenAPI spec as authoritative** and flags the
> disagreement. There are three such disagreements documented below.

---

# PROVIDER 1 — Atlassian (Jira Cloud + Confluence Cloud)

## 1.1 Authentication model for a Go service

Atlassian Cloud uses **OAuth 2.0 3LO (three-legged OAuth)**.

| Step | Detail |
|---|---|
| Authorize | `https://auth.atlassian.com/authorize` — requires `audience=api.atlassian.com`, `response_type=code`, `prompt=consent`, `state`, `scope` |
| Token exchange | `POST https://auth.atlassian.com/oauth/token` with `grant_type=authorization_code` |
| Refresh | `POST https://auth.atlassian.com/oauth/token` with `grant_type=refresh_token` |
| Refresh token lifetime | **Rotating** refresh tokens. Inactivity expiry **90 days**; reuse leeway **10 minutes**. Each refresh returns a new refresh token and **invalidates the one you used**. Add `offline_access` to the authorize `scope` to receive a refresh token. |
| Cloud ID lookup | `GET https://api.atlassian.com/oauth/token/accessible-resources` with `Authorization: Bearer <access_token>` |
| API base (Jira) | `https://api.atlassian.com/ex/jira/{cloudId}/{api}` |
| API base (Confluence) | `https://api.atlassian.com/ex/confluence/{cloudId}/{api}` |

Source: [OAuth 2.0 (3LO) apps — Jira Cloud platform](https://developer.atlassian.com/cloud/jira/platform/oauth-2-3lo-apps/)
(the same page content is published for Confluence at
[Confluence Cloud OAuth 2.0 (3LO) apps](https://developer.atlassian.com/cloud/confluence/oauth-2-3lo-apps/)).

**Critical for Go:** calls must go through `api.atlassian.com/ex/...`, **not**
`https://your-domain.atlassian.net/...`. The API reference examples use the
`your-domain.atlassian.net` form and Atlassian explicitly warns they must be rewritten.
This applies to webhook registration too.

> **ATLASSIAN-CONNECT JWT:** you do **not** need it. Connect-style JWT (`Authorization: JWT <token>`,
> HS256 signed with the app `sharedSecret` — see
> [Understanding JWT for Connect apps](https://developer.atlassian.com/cloud/jira/platform/understanding-jwt/))
> is for **Connect apps**. An OAuth 2.0 3LO app calls the REST API with
> `Authorization: Bearer <access_token>`. Connect can no longer be published on the
> Marketplace and all new extensibility features are Forge-only, so Connect is not a
> viable path for a new integration in 2026.

---

## 1.2 Dynamic webhooks via `/rest/api/3/webhook`

This is the correct API for an OAuth 2.0 app. Five operations exist (confirmed in spec):

| Method | Path | Operation |
|---|---|---|
| `POST` | `/rest/api/3/webhook` | Register dynamic webhooks |
| `GET` | `/rest/api/3/webhook` | Get dynamic webhooks for app (`startAt`, `maxResults`, default 0/100) |
| `DELETE` | `/rest/api/3/webhook` | Delete webhooks by ID |
| `PUT` | `/rest/api/3/webhook/refresh` | Extend webhook life |
| `GET` | `/rest/api/3/webhook/failed` | Get failed webhooks |

Full URL for a Go service:
`https://api.atlassian.com/ex/jira/{cloudId}/rest/api/3/webhook`

### Registration request body

```json
{
  "url": "https://your-app.example.com/webhook-received",
  "webhooks": [
    {
      "events": ["jira:issue_created", "jira:issue_updated"],
      "fieldIdsFilter": ["summary", "customfield_10029"],
      "jqlFilter": "project = PROJ"
    },
    {
      "events": ["jira:issue_deleted"],
      "jqlFilter": "project IN (PROJ, EXP) AND status = done"
    },
    {
      "events": ["issue_property_set"],
      "issuePropertyKeysFilter": ["my-issue-property-key"],
      "jqlFilter": "project = PROJ"
    }
  ]
}
```

Schema (`WebhookRegistrationDetails` → `WebhookDetails`), from the OpenAPI spec:

- `url` (**required**) — one URL per app. Spec description: *"This URL must use the same base
  URL as the Connect app. Only a single URL per app is allowed to be registered."* Note this
  wording is Connect-oriented.
- `webhooks[]` (**required**), each with:
  - `events` (**required**) — enum, see below
  - `jqlFilter` (**required by schema**) — see the caveat below
  - `fieldIdsFilter` (optional) — only triggers `jira:issue_updated` when the changelog
    contains one of these fields
  - `issuePropertyKeysFilter` (optional) — only triggers `issue_property_set` /
    `issue_property_deleted` for these keys

**Supported `events` enum (exactly these 20):**
`jira:issue_created`, `jira:issue_updated`, `jira:issue_deleted`,
`comment_created`, `comment_updated`, `comment_deleted`,
`issue_property_set`, `issue_property_deleted`,
`sprint_created`, `sprint_updated`, `sprint_closed`, `sprint_deleted`, `sprint_started`,
`jira:version_released`, `jira:version_unreleased`, `jira:version_created`,
`jira:version_moved`, `jira:version_updated`, `jira:version_merged`, `jira:version_deleted`.

Events outside this list (e.g. `attachment_created`, `worklog_created`, `project_created`)
**cannot** be registered dynamically — only via a Connect/Forge app descriptor.

### ⚠️ Doc inconsistency #1 — filter field names

The webhooks guide prose refers to `updatedFieldIdsFilter` and
`updatedIssuePropertiesFilter`. **Those names do not exist in the API.** A literal string
count over the official Jira OpenAPI spec gives:

| Name | Occurrences in spec |
|---|---|
| `fieldIdsFilter` | 3 |
| `updatedFieldIdsFilter` | **0** |
| `issuePropertyKeysFilter` | 3 |
| `updatedIssuePropertiesFilter` | **0** |

**Use `fieldIdsFilter` and `issuePropertyKeysFilter`.** The prose is stale.

### `jqlFilter` semantics

- **Required by the schema** for every webhook entry, even for `sprint_*` and
  `jira:version_*` events where it is **ignored** (Atlassian: *"you can provide any valid JQL
  expression (for example, `project = KEY`) to satisfy the schema; it will not change which
  sprint/version events you receive"*).
- Supported LHS clauses: `issueKey`, `project`, `issuetype`, `status`, `priority`,
  `assignee`, `reporter`, `issue.property`, `cf[id]` (only the Epic custom field ID).
- Supported operators: `=`, `!=`, `IN`, `NOT IN`.
- An empty JQL matches all issues from all projects and *"may reveal sensitive information"*.

### Registration response

```json
{
  "webhookRegistrationResult": [
    { "createdWebhookId": 1000 },
    { "createdWebhookId": 1001 },
    { "errors": ["The clause watchCount is unsupported"] }
  ]
}
```

Spec (`ContainerForRegisteredWebhooks` → `RegisteredWebhook`): results are returned
**in the same order as the request**. Each element has either `createdWebhookId` (int64) or
`errors` (string array). HTTP status is `200`. `400` = invalid request, `403` = caller isn't an app.

### ⚠️ Doc inconsistency #2 — response envelope

The webhooks guide shows the response as a **bare JSON array** `[{"createdWebhookId":1000}, ...]`.
The OpenAPI spec and its `example` show the **wrapped object**
`{"webhookRegistrationResult":[...]}`. The wrap is what the schema declares. **A Go client
should handle the wrapped form and tolerate a bare array defensively** (the guide's form has
been observed in the wild historically).

### Refresh (keep-alive) — mandatory

- Dynamic webhooks **expire after 30 days** from creation or last refresh.
- `PUT /rest/api/3/webhook/refresh` with `{"webhookIds":[1000,1001]}` extends each by another 30 days.
- Webhooks remain retrievable for up to **3 months after** expiry; you may refresh them then
  rather than keeping them alive continuously.
- Unrecognized/foreign webhook IDs are silently ignored.

**A production Go service must run a scheduled refresh well inside the 30-day window.**

### Delete

`DELETE /rest/api/3/webhook` with body `{"webhookIds":[1000,1001]}`.
Non-existent IDs are ignored. Only webhooks registered by the calling app are removed.

### Registration limits (OAuth 2.0)

> *"A maximum of 100 webhooks per app per tenant is allowed for a Connect app. For an
> OAuth 2.0 app, the limit is **5 webhooks per app per user on a tenant**."*

That 5-per-user limit is tight — design the registration strategy around it.

### Scopes for the webhook API

From the spec's `x-atlassian-oauth2-scopes` — **all five webhook operations** require, in
"Current" state:

```
read:jira-work
manage:jira-webhook
```

Beta/granular alternatives exist (`write:webhook:jira`, `read:webhook:jira`,
`delete:webhook:jira`) but Atlassian's scope doc says granular scopes should be used
*"only when you can't use classic scopes"* → **use the classic pair**.

Sources: [Jira Cloud platform REST API v3 — Webhooks API group](https://developer.atlassian.com/cloud/jira/platform/rest/v3/api-group-webhooks/),
[Jira Cloud Webhooks guide](https://developer.atlassian.com/cloud/jira/platform/webhooks/),
[Jira Cloud v3 OpenAPI spec](https://developer.atlassian.com/cloud/jira/platform/swagger-v3.v3.json),
[Scopes for OAuth 2.0 (3LO) and Forge apps](https://developer.atlassian.com/cloud/jira/platform/scopes-for-oauth-2-3LO-and-forge-apps/).

---

## 1.3 OAuth2 app-scoped `webhooks` component vs `rest/api/3/webhook`

These are **two different mechanisms** and the distinction matters:

| | Connect/Forge app descriptor `webhooks` module | `POST /rest/api/3/webhook` (dynamic) |
|---|---|---|
| Who can use | Connect apps (descriptor) / Forge apps | **Connect and OAuth 2.0 3LO apps** |
| Registration | Static, in `atlassian-connect.json` / Forge manifest | Runtime REST call |
| Event set | Full Jira event catalogue | Fixed 20-event subset |
| JQL support | `filter` property on the module | `jqlFilter` per webhook |
| Expiry | None | **30 days**, must `PUT .../refresh` |
| Limit | n/a | **100/app/tenant** (Connect), **5/app/user/tenant** (OAuth 2.0) |
| Delivery auth | JWT in `Authorization` header signed with `sharedSecret` | Bearer token signed with the app **client secret** |
| Publishable in 2026? | **No** — Connect apps can no longer be published to Marketplace | Yes |

For an OAuth 2.0 3LO Go service, **`rest/api/3/webhook` is the only option**; the app-scoped
`webhooks` descriptor component is not available to it.

Source: [Jira Cloud Webhooks guide](https://developer.atlassian.com/cloud/jira/platform/webhooks/).

---

## 1.4 Delivery verification — EXACT security model

**Atlassian does not HMAC-sign dynamic webhook bodies with a shared secret the way GitHub
does. There is no shared secret for dynamic webhooks, and the registration body has no
`secret` field** (confirmed: the `WebhookDetails` schema exposes only `events`,
`jqlFilter`, `fieldIdsFilter`, `issuePropertyKeysFilter`).

### What a dynamic webhook receiver actually gets

**Headers:**

| Header | Meaning | Source |
|---|---|---|
| `X-Atlassian-Webhook-Identifier` | *"Every webhook contains the `X-Atlassian-Webhook-Identifier` header that provides an identifier for a webhook. This identifier is unique within a Jira Cloud t[enant]"* | Webhooks guide, Retry policy |
| `Authorization` | `Bearer <token>`, *"signed with the app's client secret"* | Webhooks guide, "Webhooks authentication for OAuth 2.0 apps" |
| `Content-Type` | JSON | — |

**Body** (no signature, no HMAC, no timestamp header):

```json
{
  "timestamp": 1606480436302,
  "webhookEvent": "jira:issue_updated",
  "issue_event_type_name": "issue_generic",
  "user": { "...": "condensed user shape; always present for issue events; has accountType" },
  "issue": { "...": "same shape as GET issue with NO expand params" },
  "changelog": { "id": 10124, "items": [ { "field": "summary", "fromString": "...", "toString": "...", "fieldtype": "jira" } ] },
  "matchedWebhookIds": [1000, 1001],
  "bulkOperationMetaData": { "sendMail": false }
}
```

- `matchedWebhookIds` is a **dynamic-webhook-only** root property listing which of your
  registered webhooks matched. Use it to route.
- `changelog` is present **only** for `jira:issue_updated`.
- `bulkOperationMetaData` appears when a bulk operation triggered the event.
- Bodies **larger than 25 MB are not delivered**.

### The security model in one sentence

For an **OAuth 2.0 3LO app**, authenticity is established **solely by verifying the JWT/bearer
token in the `Authorization` header against your app's client secret** — not by an HMAC over
the body. Atlassian's exact words:

> *"Webhooks for OAuth 2.0 apps are secured by bearer authentication. The token is present in
> the Authorization header and is signed with the app's client secret. Remember to verify the
> token to keep your integration secure. The use of a library to verify the token is
> recommended. A list of suitable libraries can be found on the JWT website."*

**[UNCONFIRMED]** Atlassian's webhooks page does **not** state the JWT algorithm, the exact
claim set, the `iss`/`aud` values, or the token lifetime for this OAuth 2.0 webhook token.
It only says it is signed with the client secret. Do not assume HS256 — verify empirically
against a real delivery before shipping, and validate signature + `exp` + `iss`/`aud` once
the claim layout is observed.

### What about `X-Hub-Signature`?

The webhooks guide **does** describe an HMAC scheme — secret token, `method: sha256`, and a
header of the form `X-Hub-Signature: sha256=<hex hmac>` — with the test vector
`secret="It's a Secret to Everybody"`, `payload="Hello World!"` →
`a4771c39fbe90f317c7824e83ddef3caae9cb3d976c214ace1f2937e133263c9`.

**This applies only to "Secure admin webhooks"** — webhooks created in **Jira Administration**
or registered via `/rest/webhooks/1.0/webhook`, which accept an optional `secret` field.
It does **not** apply to dynamic webhooks registered through `/rest/api/3/webhook`.

**Practical recommendation for the Go service:** since dynamic webhooks have no body HMAC,
harden as follows — (1) verify the `Authorization` bearer JWT with the client secret;
(2) treat the endpoint URL as a capability secret (unguessable path segment);
(3) restrict inbound traffic to Atlassian's published IP ranges, which the guide requires you
to allow-list anyway; (4) deduplicate on `X-Atlassian-Webhook-Identifier` + `timestamp`
because **deliveries are at-least-once**.

### Retry / idempotency

Up to **5 retries**, randomized backoff **5–15 minutes** between attempts. Retried on
`408, 409, 425, 429, 5xx`, connection failure, or timeout. Return **HTTP 200** on success;
anything else is treated as a failure. Duplicates are possible — **make handlers idempotent**.

Sources: [Jira Cloud Webhooks guide](https://developer.atlassian.com/cloud/jira/platform/webhooks/).

---

## 1.5 Jira Cloud REST API v3 — actions, exact calls and scopes

Base for a Go service: `https://api.atlassian.com/ex/jira/{cloudId}/rest/api/3/...`
Media type: `Content-Type: application/json`, `Accept: application/json`.
Rich text fields (`description`, comment `body`, etc.) use **Atlassian Document Format (ADF)**,
i.e. `{"type":"doc","version":1,"content":[...]}`, in v3.

Scopes below marked **Current** are the classic scopes from the spec's
`x-atlassian-oauth2-scopes`; **Beta** are the granular replacements.

### Create issue
- `POST /rest/api/3/issue`
- Body: `IssueUpdateDetails` → `{"fields": {...}}`; optional `update` for verb actions; optional `properties`.
- Example: `{"fields":{"project":{"key":"PROJ"},"issuetype":{"id":"10001"},"summary":"...","description":{ADF},"assignee":{"id":"<accountId>"},"duedate":"2019-05-11"}}`
- Query: `updateHistory` (bool)
- Response `201` `CreatedIssue` → `{"id":"10000","key":"PROJ-1","self":"..."}`
- Scope: **`write:jira-work`** (Beta: `write:issue:jira`, …)

### Get issue (and resolving an issue key)
- `GET /rest/api/3/issue/{issueIdOrKey}`
- **Key resolution:** `issueIdOrKey` accepts **either a numeric ID or a key**; there is no
  separate "resolve key" call. Spec: *"if the identifier doesn't match an issue, a
  case-insensitive search and check for moved issues is performed. If a matching issue is
  found its details are returned, a 302 or other redirect is **not** returned. The issue key
  returned in the response is the key of the issue found."*
  → For a "what is this key's ID?" lookup, call `GET /rest/api/3/issue/{key}?fields=id` and
  read `id`/`key` from the response.
- Query params: `fields` (default `*all`; use `fields=id` to minimise payload), `fieldsByKeys`, `expand`, `properties`, `updateHistory`, `failFast`
- Scope: **`read:jira-work`**

### Update issue
- `PUT /rest/api/3/issue/{issueIdOrKey}`
- Body: `IssueUpdateDetails` → `{"fields":{...}}` (set) and/or `{"update":{...}}` (add/remove/set ops), plus optional `historyMetadata`, `properties`
- Query: `notifyUsers`, `overrideScreenSecurity`, `overrideEditableFlag`, `returnIssue`, `expand`
- Response: `204` (empty) by default; issue body if `returnIssue=true`
- **Transition is not supported here and is ignored** — use the transitions endpoint
- Scope: **`write:jira-work`**

### Transition issue (+ discovering transitions)
Discover first:
- `GET /rest/api/3/issue/{issueIdOrKey}/transitions` — returns transitions available *to the
  current user from the issue's current status*. Query: `expand=transitions.fields` to get the
  screen fields for each transition.
- Response shape: `{"expand":"transitions","transitions":[{"id":"5","name":"Resolve Issue","to":{"id":"5","name":"Resolved","statusCategory":{...}},"hasScreen":false,"isGlobal":false,"isInitial":false,"isAvailable":true,"isConditional":false,"fields":{...}}]}`
- Scope: **`read:jira-work`**

Then execute:
- `POST /rest/api/3/issue/{issueIdOrKey}/transitions`
- Body: `{"transition":{"id":"5"}, "fields":{...}, "update":{...}}` — `transition.id` is
  **required**; `fields`/`update` populate the transition screen if there is one
- Example (with comment): `{"transition":{"id":"5"},"update":{"comment":[{"add":{"body":{ADF}}}]}}`
- Response: `204`
- Scope: **`write:jira-work`**

**Gotcha:** the transition ID is workflow-specific. Cache per project+issuetype+status and
re-resolve; never hard-code.

### Add comment
- `POST /rest/api/3/issue/{issueIdOrKey}/comment`
- Body: `{"body": {ADF}}`, optional `visibility` (`{"type":"role","value":"Administrators"}`),
  `properties`
- Query: `expand`
- Response `201` `Comment` → `{"id":"10000","self":"...","body":{ADF},"author":{...},"created":"..."}`
- Scope: **`write:jira-work`**

### Assign issue
- `PUT /rest/api/3/issue/{issueIdOrKey}/assignee`
- Body: `{"accountId":"5b10ac8d82e05b22cc7d4ef5"}`
- Special values (documented): `"-1"` → default assignee for the project; `null` → unassign.
  (Spec text also mentions `name`, but **Jira Cloud requires `accountId`**; `name` is
  deprecated/removed. Use `accountId`.)
- Response: `204`
- Scope: **`write:jira-work`**
- Note: this endpoint exists for callers who have *Assign issues* but **not** *Edit issues*.

### Search with JQL — USE `/rest/api/3/search/jql`

> **⚠️ The legacy endpoint is GONE.** Atlassian's KB states verbatim: *"The legacy endpoint
> `/rest/api/3/search` is deprecated and has been fully removed from Jira Cloud. Please
> migrate all uses of this endpoint to `/rest/api/3/search/jql` to avoid service
> disruptions."* The OpenAPI spec marks both `GET /rest/api/3/search` and
> `POST /rest/api/3/search` as `deprecated: true` with the description *"Endpoint is currently
> being removed."* (changelog **CHANGE-2046**).

**Current endpoints (not deprecated):**
- `GET /rest/api/3/search/jql`
- `POST /rest/api/3/search/jql` (operation id `searchAndReconsileIssuesUsingJqlPost`) — use this
  for large JQL or when you need `reconcileIssues`

**Request parameters** (`SearchAndReconcileRequestBean`), GET query / POST body:
| Field | Notes |
|---|---|
| `jql` | **Must be a bounded query** (performance requirement). Default page size is **50**, not 100. |
| `fields` | Array. **Default is `id` only** — you must ask for `summary`, `status`, etc. (`*all`, `*navigable`, or explicit list; `-field` to exclude) |
| `expand` | Comma-delimited string (`renderedFields`, `names`, `schema`, `transitions`, `operations`, `editmeta`, `changelog`, `versionedRepresentations`) |
| `properties` | Up to 5 issue property keys |
| `fieldsByKeys` | Default `false` |
| `failFast` | Default `false` |
| `maxResults` | Default 50 |
| `nextPageToken` | Cursor for the next page |
| `reconcileIssues` | Up to 50 issue IDs for strong read-after-write consistency |
| `includeArchivedProjects` | Default `false` |

**Response** (`SearchAndReconcileResults`):
```json
{
  "issues": [ { "id": "...", "key": "...", "fields": { ... } } ],
  "isLast": true,
  "nextPageToken": null,
  "names": { ... },
  "schema": { ... },
  "warnings": [ ... ]
}
```

**Pagination changed shape:** cursor-based `nextPageToken` / `isLast`. **`startAt` and `total`
are gone** — a Go client that pages with `startAt` must be rewritten. `nextPageToken` **expires
after 7 days**. Because `isLast` can be absent, page until `nextPageToken` is null/empty.

Scope: **`read:jira-work`**

Related: `POST /rest/api/3/search/approximate-count` (body `{"jql":"project = HSP"}`) gives an
estimated count and requires a bounded JQL. Not deprecated.

Sources: [REST API v3 search group](https://developer.atlassian.com/cloud/jira/platform/rest/v3/api-group-issue-search/),
[Jira KB: Run JQL search query using Jira Cloud REST API](https://confluence.atlassian.com/jirakb/run-jql-search-query-using-jira-cloud-rest-api-1289424308.html).

### List projects
- **Recommended (paginated):** `GET /rest/api/3/project/search` — supports query, pagination, `expand`, `recent`, `properties`
- **Deprecated:** `GET /rest/api/3/project` — spec marks `deprecated: true`, description says
  *"Deprecated, use Get projects paginated that supports search and pagination."* → **do not use**
- Single project: `GET /rest/api/3/project/{projectIdOrKey}` (not deprecated) → includes `id`, `key`, `name`, `issueTypes`
- Scope: **`read:jira-work`**

### List issue types
- All issue types: `GET /rest/api/3/issuetype` (operation `getIssueAllTypes`)
- Per project (usually what you want, since schemes vary): `GET /rest/api/3/issuetype/project?projectId={id}`
- Create metadata (fields available per type): `GET /rest/api/3/issue/createmeta/{projectIdOrKey}/issuetypes`
  and `GET /rest/api/3/issue/createmeta/{projectIdOrKey}/issuetypes/{issueTypeId}`
- Scope: **`read:jira-work`**

### Delete issue
- `DELETE /rest/api/3/issue/{issueIdOrKey}`
- Query: `deleteSubtasks` (`true`/`false`) — **required if the issue has subtasks**, otherwise
  the delete fails
- Response: `204`
- Scope: **`write:jira-work`**
- Note: "This operation can be accessed anonymously" refers to permission semantics; you still
  need a valid token.

### Scope summary for the Go service (classic scopes — recommended)

| Action | Endpoint | Scope |
|---|---|---|
| Register/list/delete/refresh webhooks | `/rest/api/3/webhook*` | `read:jira-work` + `manage:jira-webhook` |
| Create/update/assign/comment/transition/delete issue | `/rest/api/3/issue*` | `write:jira-work` |
| Get issue, list projects, list issue types, transitions lookup, JQL search | GET endpoints | `read:jira-work` |

Minimal viable set: **`read:jira-work`, `write:jira-work`, `manage:jira-webhook`, `offline_access`**.

---

## 1.6 Confluence Cloud — brief

**Can the same OAuth 2.0 token reach Confluence?** Yes, **if** the Confluence scopes were
granted in the same authorization grant. `GET /oauth/token/accessible-resources` returns one
entry per site with a `scopes` array; a site entry listing Confluence scopes
(e.g. `write:confluence-content`, `read:confluence-content.all`) confirms Confluence access.
The `id` field of the entry is the **`cloudId`**, shared by Jira and Confluence for that site.

**Base URL patterns (official):**
```
Jira:       https://api.atlassian.com/ex/jira/{cloudId}/{api}
Confluence: https://api.atlassian.com/ex/confluence/{cloudId}/{api}
```
with `{api}` given as e.g. `/rest/api/space` for Confluence.

**⚠️ [PARTIALLY UNCONFIRMED] — the `/wiki` prefix.** The Confluence Cloud OpenAPI specs
(v1 and v2) declare `servers: [{"url": "//your-domain.atlassian.net"}]` and **all** paths are
prefixed `/wiki/rest/api/...` and `/wiki/api/v2/...`. Combining spec + 3LO doc implies:

```
https://api.atlassian.com/ex/confluence/{cloudId}/wiki/rest/api/...
```

but Atlassian's 3LO page writes the `{api}` example as `/rest/api/space` **without** `/wiki`.
These two official sources disagree. **Verify empirically against a real tenant before
committing to a path in Go** — this is a known source of 401/404 confusion for Confluence 3LO.

**cloudId source:** `GET https://api.atlassian.com/oauth/token/accessible-resources`
(`Authorization: Bearer <access_token>`, `Accept: application/json`) → array of
`{"id","name","url","scopes":[...],"avatarUrl"}`. Take `id`.

**Confluence Cloud webhooks:** **[UNCONFIRMED that a REST API exists — evidence says it does NOT.]**
- The Confluence Cloud v1 REST API docs have **no Webhooks API group**; requesting
  `.../rest/v1/api-group-webhooks/` returns the generic API landing page, and the published
  Confluence OpenAPI specs contain **zero** paths matching `webhook`.
- Confluence Cloud webhooks are declared in the **Connect app descriptor** `webhook` module
  ([Confluence Webhook module](https://developer.atlassian.com/cloud/confluence/modules/webhook/)),
  receiving atlassian-connect.json-declared events with a JWT in the `Authorization` header.
  Connect delivery is *"best effort"* and *"not guaranteed"* per that page.
- There is **no `/rest/api/3/webhook` equivalent for Confluence Cloud** and no
  `/rest/api/webhook` in the official specs.

→ **A Confluence Cloud webhook cannot be registered with a 3LO OAuth2 token via REST.**
Confluence actions via 3LO REST work fine; Confluence eventing effectively requires
Forge/Connect (or polling). If dynamic Confluence webhooks are a hard requirement, that must
change the integration design.

---

## 1.7 Atlassian — open items / things not confirmed

1. **OAuth2 webhook JWT details** — algorithm, claims, `iss`/`aud`, lifetime are **not
   documented**. Only "signed with the app's client secret". Must be determined empirically.
2. **Confluence 3LO path prefix** — `/wiki` vs no `/wiki`; official sources conflict (§1.6).
3. **`webhookRegistrationResult` envelope** — spec says wrapped object, guide shows bare array (§1.2).
4. **`fieldIdsFilter` vs `updatedFieldIdsFilter`** — prose is wrong, spec is right (§1.2).
5. **Exact `CHANGE-2046` removal date** — the KB confirms removal has already happened; the
   changelog page is JS-rendered and could not be read directly for the precise date.
6. **Webhook URL restriction for OAuth 2.0 apps** — the spec's "must use the same base URL as
   the Connect app" wording is Connect-specific; what constraint applies to a 3LO app's
   webhook URL is not stated. **[UNCONFIRMED]**

---
---

# PROVIDER 2 — HubSpot

> **Scope note:** HubSpot's docs are Mintlify-hosted. Appending `.md` to any docs URL returns
> clean markdown (the HTML version is truncated/JS-rendered), and
> `https://developers.hubspot.com/docs/llms.txt` enumerates every page. Almost every API
> reference page **embeds the full OpenAPI YAML inline**, which is the most authoritative
> source available and is what this section relies on. HubSpot does *not* serve the raw
> `/docs/specs/**.json` files over HTTP (they 404) — the embedded YAML is the accessible form.

## 2.1 API versioning — v3 is now "legacy"

HubSpot has moved to **dated API versions**. The `/v3/` prefix seen in older guides is filed
under `/docs/api-reference/legacy/`. Current as of 2026:

| Layer | Current | Notes |
|---|---|---|
| Platform (project) version | **`2026.09`** (Current, released 2026-09-08) | `2026.03` Supported; `2025.2` Supported; `2025.1` sunset 2026-08-01 |
| REST path prefix | **`/2026-09/`** | e.g. `/crm/objects/2026-09/contacts` |
| Beta | `/2027-03-beta/` | a 2027-03 release is already in beta |

Sources: [Platform versioning](https://developers.hubspot.com/docs/developer-tooling/platform/versioning.md),
[`_llms/apis/2026-09.md`](https://developers.hubspot.com/docs/_llms/apis/2026-09.md).

**For a new Go service, build against `2026-09` paths.** The `v3` paths still work and are not
formally deprecated, but they are the previous generation. **The task brief assumed `/crm/v3/...`
— that assumption is outdated.**

---

## 2.2 Webhooks: how they are configured

There are **three app types with three different mechanisms** — this is the single most
important thing to get right:

| App type | How webhooks are configured | API-manageable? |
|---|---|---|
| **Legacy private app** | **UI only.** | ❌ **No** |
| **Legacy public app** | UI **or** REST API | ✅ Yes |
| **Project-based app** (2026.09 platform) | Declarative `src/app/webhooks/*-hsmeta.json` | ❌ Not via subscription endpoints (use the separate journal API, §2.3) |

**Private apps — verbatim from HubSpot:**

> *"Managing your private app's webhook subscriptions via API is not currently supported.
> Subscriptions can only be managed in your private app settings."*
> — [Create and edit webhook subscriptions in private apps](https://developers.hubspot.com/docs/apps/legacy-apps/private-apps/create-and-edit-webhook-subscriptions-in-private-apps.md)

Also: *"In private apps, webhook settings can only be edited in the in-app settings for your
private app, and cannot currently be edited through the API."* Limit: **1000 subscriptions
per private app.** Legacy private apps created with projects do not support webhook
subscriptions at all.

**Legacy public apps — API-manageable** (the `/webhooks/v3/{appId}/...` and
`/app-webhooks/2026-09/{appId}/...` families, §2.3).

**Project-based apps — declarative config only.** Create `src/app/webhooks/webhook-hsmeta.json`:

```json
{
  "uid": "webhooks",
  "type": "webhooks",
  "config": {
    "settings": { "targetUrl": "https://example.com/webhook", "maxConcurrentRequests": 10 },
    "subscriptions": {
      "crmObjects":       [ { "subscriptionType": "object.creation", "objectType": "contact", "active": true } ],
      "legacyCrmObjects": [ { "subscriptionType": "contact.propertyChange", "propertyName": "lastname", "active": true } ],
      "hubEvents":        [ { "subscriptionType": "contact.privacyDeletion", "active": true } ]
    }
  }
}
```
- `crmObjects` → the new `object.*` format (with an `objectType` field)
- `legacyCrmObjects` → classic types like `contact.creation`, `deal.deletion`
- `hubEvents` → only `contact.privacyDeletion` and `conversation.*`

Source: [Configure a webhook subscription](https://developers.hubspot.com/docs/apps/developer-platform/add-features/configure-webhooks.md).

**⚠️ Design consequence:** if the Go service needs to create subscriptions **programmatically**
at runtime, it must be a **legacy public app** (or you use the separate journal API, §2.3).
A private app cannot do it, and a project-based app cannot do it via these endpoints.

---

## 2.3 The subscriptions REST API — v3 (legacy) and 2026-09 (current)

**Host for both: `https://api.hubapi.com`.**

### Current generation — `/app-webhooks/2026-09/` (VERIFIED from embedded OpenAPI)

| Method | Path |
|---|---|
| `POST` | `/app-webhooks/2026-09/{appId}/subscriptions` → **201** + `Location` header |
| `GET` | `/app-webhooks/2026-09/{appId}/subscriptions` |
| `GET` | `/app-webhooks/2026-09/{appId}/subscriptions/{subscriptionId}` |
| `PATCH` | `/app-webhooks/2026-09/{appId}/subscriptions/{subscriptionId}` |
| `DELETE` | `/app-webhooks/2026-09/{appId}/subscriptions/{subscriptionId}` |
| `POST` | `/app-webhooks/2026-09/{appId}/subscriptions/batch/update` (batch create) |
| `GET` / `PUT` / `DELETE` | `/app-webhooks/2026-09/{appId}/settings` |

Verified by fetching `https://developers.hubspot.com/docs/api-reference/latest/webhooks/subscriptions/create-webhook-appId.md`
and reading the embedded spec (`POST /app-webhooks/2026-09/{appId}/subscriptions`,
`operationId: post-/app-webhooks/2026-09/{appId}/subscriptions_create`, `servers: https://api.hubapi.com`).

**Create body — `SubscriptionCreateRequest`** (2026-09 requires **both** `active` and `eventType`):
```json
{ "active": true, "eventType": "contact.propertyChange", "propertyName": "email" }
```
- `eventType` (**required**) — enum, §2.4
- `active` (**required** in 2026-09; optional in legacy v3, defaults to `false`)
- `propertyName` (optional) — only for `propertyChange` types
- `objectTypeId` (optional) — for custom objects
- `eventTypeName` (optional) — for custom-object event types

**Response — `SubscriptionResponse`** (201): required `active`, `createdAt`, `eventType`, `id`;
optional `eventTypeName`, `objectTypeId`, `propertyName`, `updatedAt`.

**Settings:** `SettingsChangeRequest` = `{"targetUrl": "https://...", "throttling": {"maxConcurrentRequests": 10}}`.
Both fields required; `maxConcurrentRequests` must be **greater than 5**.
Settings can be **cached for up to 5 minutes**.

### Legacy generation — `/webhooks/v3/`

**`POST /webhooks/v3/{appId}/subscriptions` DOES exist** — verified from the embedded OpenAPI at
[Create an event subscription (legacy)](https://developers.hubspot.com/docs/api-reference/legacy/webhooks/subscriptions/create-subscription.md),
which declares `POST /webhooks/v3/{appId}/subscriptions`, `servers: https://api.hubapi.com`, and:

```yaml
security:
  - oauth2: [developers-write]
  - oauth2: [private-apps-write]
```

Full legacy family: `POST`/`GET /webhooks/v3/{appId}/subscriptions`,
`GET`/`PATCH`/`DELETE /webhooks/v3/{appId}/subscriptions/{subscriptionId}`,
`POST /webhooks/v3/{appId}/subscriptions/batch/update`,
`GET`/`PUT`/`DELETE /webhooks/v3/{appId}/settings`.

The legacy `SubscriptionCreateRequest` requires only `eventType`.

### ⚠️ Auth conflict for these endpoints — unresolved

The **OpenAPI security block** on both the v3 and 2026-09 pages declares OAuth2 scopes
`developers-write` / `developers-read` (app management) or `private-apps-write` /
`private-apps-read`. But the **prose guide** says:

> *"The endpoints described below are authenticated using your Developer API key, which should
> be included via the `?hapikey` query parameter."*

These contradict each other. **[UNCONFIRMED]** which is correct in 2026, and in particular
whether a private app's OAuth token can call `/app-webhooks/2026-09/{its own appId}/...`
(the private-app docs say subscription management via API is unsupported, which suggests not).
**Verify against a live app before designing the Go client around either.**

### Also available: Webhooks Journal & v4 management API (BETA)

A **separate, newer** system with a completely different taxonomy. Auth is an **OAuth 2.0
client-credentials token** (app-level, not user-level) — not a user access token:

- Token: `https://api.hubapi.com/oauth/v1/token` (client credentials) → `Authorization: Bearer <token>`
- Scopes: `developer.webhooks_journal.read`, `developer.webhooks_journal.subscriptions.read`,
  `developer.webhooks_journal.subscriptions.write`, `developer.webhooks_journal.snapshots.read`,
  `developer.webhooks_journal.snapshots.write`, **plus** the object scopes (e.g. `crm.contacts.read`)
- Create: `POST /webhooks-journal/subscriptions/v4`
  ```json
  { "objectTypeId": "0-1", "subscriptionType": "OBJECT", "portalId": 12345,
    "actions": ["CREATE","UPDATE","DELETE"], "properties": ["email","firstname","lastname"],
    "objectIds": [1001,1002,1003] }
  ```
- Read: `GET /webhooks-journal/subscriptions/v4`; delete: `DELETE /webhooks-journal/subscriptions/v4/{subscriptionId}`
  or `DELETE /webhooks-journal/subscriptions/v4/portals/{portalId}`
- Journal reads: `GET /webhooks-journal/journal/v4/earliest|latest|offset/{offset}/next` → returns a signed URL to a JSON file
- `subscriptionType` ∈ `OBJECT`, `ASSOCIATION`, `APP_LIFECYCLE_EVENT`, `LIST_MEMBERSHIP`; `actions` ∈
  `CREATE`, `UPDATE`, `DELETE`, `MERGE`, `RESTORE`, `ASSOCIATION_ADDED`, `ASSOCIATION_REMOVED`, `SNAPSHOT`
- Common `objectTypeId`s: contacts `0-1`, companies `0-2`, deals `0-3`, engagements `0-4`, tickets `0-5`, products `0-7`, line items `0-8`, conversations `0-11`, tasks `0-27`. Custom objects use metaTypeId `2`.
- **v4 is NOT compatible with v3/2026-09** — different subscription model, and v4 has **no `targetUrl`**
  (it writes to a journal the app polls instead of pushing).
- Sources: [Webhooks journal and management APIs (BETA)](https://developers.hubspot.com/docs/api-reference/legacy/webhooks/webhooks-journal.md),
  [2026-09 journal guide](https://developers.hubspot.com/docs/api-reference/latest/webhooks-journal/guide.md)

---

## 2.4 Subscription event types + required scopes

Authoritative prose table: [Webhooks guide → Webhook subscriptions](https://developers.hubspot.com/docs/api-reference/latest/webhooks/guide.md#webhook-subscriptions).

**The request field is `eventType`** (not `subscriptionType`).

| Family | Types | Required scope |
|---|---|---|
| `contact.*` | `creation`, `deletion`, `merge`, `restore`, `privacyDeletion`, `propertyChange`, `associationChange` | `crm.objects.contacts.read` |
| `company.*` | `creation`, `deletion`, `merge`, `restore`, `propertyChange`, `associationChange` | `crm.objects.companies.read` |
| `deal.*` | `creation`, `deletion`, `merge`, `restore`, `propertyChange`, `associationChange` | `crm.objects.deals.read` |
| `ticket.*` | `creation`, `deletion`, `merge`, `restore`, `propertyChange`, `associationChange` | `tickets` |
| `product.*` | `creation`, `deletion`, `merge`, `restore`, `propertyChange` | `e-commerce` |
| `line_item.*` | `creation`, `deletion`, `merge`, `restore`, `propertyChange`, `associationChange` | `crm.objects.line_items.read` |
| `conversation.*` | `creation`, `deletion`, `privacyDeletion`, `propertyChange`, `newMessage` | `conversations.read` |
| `object.*` | `creation`, `deletion`, `merge`, `restore`, `propertyChange`, `associationChange` — **schema enum only** | **[UNCONFIRMED]** (custom objects) |
| `event.completed` | **schema enum only** | **[UNCONFIRMED]** |

Behaviour notes: `contact.privacyDeletion` **also** fires `contact.deletion` (two notifications
if subscribed to both). `associationChange` fires **two** events, one per side of the
association. `conversation.propertyChange` supports only `assignedTo`, `status`, `isArchived`.

---

## 2.5 Webhook payload envelope — field-by-field

**Envelope: a JSON array of event objects.** Verbatim from the guide: *"As shown above, you
should expect to receive an array…"*

```json
[
  {
    "objectId": 1246965,
    "propertyName": "lifecyclestage",
    "propertyValue": "subscriber",
    "changeSource": "ACADEMY",
    "eventId": 3816279340,
    "subscriptionId": 25,
    "portalId": 33,
    "appId": 1160452,
    "occurredAt": 1462216307945,
    "eventType": "contact.propertyChange",
    "attemptNumber": 0
  }
]
```
*(This is HubSpot's official example, reproduced verbatim from the guide.)*

| Field asked about in the brief | Verdict | Notes |
|---|---|---|
| `portalId` | ✅ documented | HubSpot account ID where the event occurred |
| `objectId` | ✅ documented | Contact/company/deal ID; conversation thread ID for conversations |
| `changeSource` | ✅ documented | Source of change (same values as contact property histories) |
| `eventId` | ✅ documented | **"This value is not guaranteed to be unique."** → do not use as an idempotency key alone |
| `occurredAt` | ✅ documented | Millisecond timestamp |
| `propertyName` | ✅ documented | **Only** for `propertyChange` subscriptions |
| `propertyValue` | ✅ documented | **Only** for `propertyChange` subscriptions; the new value |
| `subscriptionType` | ⚠️ **CONTRADICTION** | The field **table** lists `subscriptionType`; **both official JSON examples emit `eventType` instead** |
| `subscriptionId` | ✅ documented | ID of the subscription that triggered it |
| `attemptNumber` | ✅ documented | Starts at **0** |
| `appId` | ✅ documented | Your app ID (for multiple apps sharing a webhook URL) |
| `sourceId` | ❌ **not documented** | Appears in no official field table — **do not rely on it** |
| `changeFlag` | ❌ **not documented** | Appears in no official field table — **do not rely on it** |

**Conditional fields:** `messageId` + `messageType` (`MESSAGE`/`COMMENT`) for
`conversation.newMessage`; merge events add `primaryObjectId`, `mergedObjectIds`,
`newObjectId`, `numberOfPropertiesMoved`; association events add `associationType`,
`fromObjectId`, `toObjectId`, `associationRemoved`, `isPrimaryAssociation`.

**⚠️ The `eventType` / `subscriptionType` contradiction is a real, unresolved doc bug.**
Both official JSON examples use `eventType`; only the prose table says `subscriptionType`
(the brief's premise). **Defensive Go decoding: read `eventType`, fall back to `subscriptionType`.**

### Batching, retries, limits

- **Max 100 events per request.** *"Each request can contain up to 100 events."*
- Default concurrency: **10 in-flight requests** per installed account (`maxConcurrentRequests`, must be > 5).
- Receiver must return **2xx**. Retry triggers: **connection failure**, **timeout > 5 seconds**, or **any 4xx/5xx**.
- **Up to 10 retries, spread over the next 24 hours**, with randomized delays.
- Delivery is **unordered** and **at-least-once** — duplicates possible.
- **Max 1000 subscriptions per application** (400 error beyond that).
- Webhook `POST`s **do not** count against API rate limits.

Source: [Webhooks guide → Retries / Limits](https://developers.hubspot.com/docs/api-reference/latest/webhooks/guide.md).

---

## 2.6 Delivery verification — EXACT signature specification

**HubSpot does sign its webhook payloads with HMAC — unlike Atlassian.** Authoritative source:
[Validating Requests](https://developers.hubspot.com/docs/apps/developer-platform/build-apps/authentication/request-validation.md)
(current, for the new developer platform) and the
[legacy equivalent](https://developers.hubspot.com/docs/apps/legacy-apps/authentication/validating-requests.md).

### Headers

| Header | Content |
|---|---|
| `X-HubSpot-Signature` | v1/v2 signature — SHA-256 **hex** digest (no algorithm prefix) |
| `X-HubSpot-Signature-Version` | `v1`, `v2`, or `v3` — tells you which scheme to use |
| `X-HubSpot-Signature-V3` (also written `X-HubSpot-Signature-v3`) | the **v3** signature — **base64**-encoded HMAC-SHA256 |
| `X-HubSpot-Request-Timestamp` | millisecond epoch timestamp, used in the v3 signed string |

Requests carry **all** versions for backwards compatibility. **Validate against v3.**

### v3 signature — the exact algorithm

1. **Reject the request if the timestamp is older than 5 minutes.**
2. Take the **request URI** and:
   - **drop the fragment** (`#...`),
   - **decode only the query-string portion** — do *not* decode the path — replacing exactly
     these sequences:

     | Encoded | Decoded |
     |---|---|
     | `%3A` | `:` |
     | `%2F` | `/` |
     | `%3F` | `?` |
     | `%40` | `@` |
     | `%21` | `!` |
     | `%24` | `$` |
     | `%27` | `'` |
     | `%28` | `(` |
     | `%29` | `)` |
     | `%2A` | `*` |
     | `%2C` | `,` |
     | `%3B` | `;` |

     The docs add: *"You do not need to decode the question mark that denotes the beginning of
     the query string."* (i.e. the literal `?` separator stays as-is; an encoded `%3F`
     *within* the query **is** decoded.)
   - **include the protocol** — e.g. `https://host/path?query`.
3. Build a **UTF-8** string by concatenating, in this exact order:

   ```
   requestMethod + requestUri + requestBody + timestamp
   ```
   where `timestamp` is the raw value of `X-HubSpot-Request-Timestamp`, and `requestBody` is the
   **raw, unparsed** request body bytes.
4. Compute **HMAC-SHA256** of that string using the **app's client secret** as the key.
5. **Base64-encode** the digest (not hex).
6. Compare against `X-HubSpot-Signature-V3` using a **constant-time** comparison.

**Go note:** the request URI must be reconstructed exactly as HubSpot sent it. Read the raw
request line / `r.URL.RequestURI()` with `r.Host` rather than re-serializing from parsed
components, and validate the HMAC over the **raw body bytes** (`io.ReadAll` before any JSON
decoding). Go canonicalizes the header name to `X-Hubspot-Signature-V3`, but `Header.Get` is
case-insensitive so this is safe.

### The URL-encoding detail (the one the brief flagged)

This is the most commonly-implemented-wrong part, and HubSpot **changed/clarified its guidance**:

- The **older** (legacy) page shows a naive whole-URI `str.replace` for all 12 sequences.
- The **current** page's Node/Java/PHP samples **decode only the query-string portion** and
  **drop the fragment first**, with an explicit comment *"Decode only the query string portion"*.

**Implement the current guidance.** A URL-encoded `/` in the *path* (e.g. an object ID encoded
as `%2F`) must **stay encoded** when computing the signature.

### v2 and v1 (backwards compatibility only)

- **v2** — `SHA-256(client_secret + httpMethod + URI + body)` → **hex**; compare to `X-HubSpot-Signature`.
  Used by workflow webhook actions and CRM cards. No timestamp; **no replay protection**.
- **v1** — `SHA-256(client_secret + body)` → **hex**; compare to `X-HubSpot-Signature`.
  Legacy CRM object events.

### Replay window

**5 minutes** — the only replay protection described, and it applies to **v3** only.
Reject if `now - X-HubSpot-Request-Timestamp > 300000` ms. The docs' PHP sample uses
`abs(time() - timestamp/1000) > 300`, i.e. it also rejects far-future timestamps; do the same.

---

## 2.7 CRM actions — exact calls and scopes (2026-09)

Host: `https://api.hubapi.com`. Paths below are **verified against the embedded OpenAPI** on each
reference page (each carries `servers: https://api.hubapi.com` and `version: 2026-09`).

**Scope note:** each reference page has a human-readable **"Required Scopes" panel** *and* a
machine-readable OpenAPI `security` array. The `security` array on CRM object pages
**enumerates ~80 alternative scopes** (a codegen artifact — e.g. the contacts-create page lists
81 entries including `oauth`, `crm.objects.deals.read`, etc.). **The rendered "Required Scopes"
panel is the authoritative answer**; the array is not usable as-is.

| # | Action | Method + path | Scope |
|---|---|---|---|
| 1 | Create contact | `POST /crm/objects/2026-09/contacts` → **201** + `Location` | `crm.objects.contacts.write` |
| 2 | Get contact | `GET /crm/objects/2026-09/contacts/{contactId}` | `crm.objects.contacts.read` |
| 3 | Update contact | `PATCH /crm/objects/2026-09/contacts/{contactId}` (supports `?idProperty=email`) | `crm.objects.contacts.write` |
| 4 | Search contacts | `POST /crm/objects/2026-09/contacts/search` | `crm.objects.contacts.read` |
| 5 | Create deal | `POST /crm/objects/2026-09/0-3` ⚠️ see below | `crm.objects.deals.write` |
| 6 | Create company | `POST /crm/objects/2026-09/companies` | `crm.objects.companies.write` |
| 7 | Create note | `POST /crm/objects/2026-09/notes` | **`crm.objects.contacts.write`** ⚠️ |
| 8 | List objects | `GET /crm/objects/2026-09/{contacts\|companies\|notes\|deals}` | the matching `.read` scope |

**Create body shape** (`properties` required; `associations` optional but listed in schema):
```json
{
  "properties": { "email": "a@b.com", "firstname": "Ada" },
  "associations": [
    { "to": { "id": 301 },
      "types": [ { "associationCategory": "HUBSPOT_DEFINED", "associationTypeId": 202 } ] }
  ]
}
```

**⚠️ Deals path inconsistency (verified in the official docs).** The 2026-09 deals reference
page's embedded OpenAPI declares the path literally as **`/crm/objects/2026-09/0-3`** (the
object-type ID), while the deals prose guide uses **`/crm/objects/2026-09/deals`**. I confirmed
the OpenAPI form by fetching
[create-deal.md](https://developers.hubspot.com/docs/api-reference/latest/crm/objects/deals/create-deal.md)
(`paths:` → `/crm/objects/2026-09/0-3`). **Which form the live server accepts is [UNCONFIRMED]** —
test both. (The same inconsistency exists in legacy v3: `/crm/v3/objects/0-3` vs `/crm/v3/objects/deals`.)

**⚠️ Notes scope is surprising but confirmed.** The create-note page's "Required Scopes" panel
lists **`crm.objects.contacts.write`** — not `crm.objects.notes.write`. `crm.objects.notes.read`
appears in the OpenAPI `securitySchemes` and in the associations guide, but the notes endpoints
themselves say `crm.objects.contacts.write`. **[UNCONFIRMED]** whether a dedicated notes scope
is installable.

**Notes specifics:** `hs_timestamp` is **required**; `hs_note_body` max 65,536 chars.
Association type IDs: **Note→Contact = `202`**, Note→Company = `190`, Note→Deal = `214`,
Note→Ticket = `228`.

### Search request body

```json
{
  "filterGroups": [
    { "filters": [ { "propertyName": "email", "operator": "CONTAINS_TOKEN", "value": "*@hubspot.com" } ] }
  ],
  "properties": ["annualrevenue", "name"],
  "sorts": [ { "propertyName": "createdate", "direction": "DESCENDING" } ],
  "limit": 20,
  "after": "20"
}
```
Operators: `LT`, `LTE`, `GT`, `GTE`, `EQ`, `NEQ`, `BETWEEN`, `IN`, `NOT_IN`, `HAS_PROPERTY`,
`NOT_HAS_PROPERTY`, `CONTAINS_TOKEN`, `NOT_CONTAINS_TOKEN`. Association search uses the
pseudo-property `associations.{objectType}`.

Search limits: default page 10, **max 200**; max 5 `filterGroups` × 6 `filters` (18 total);
query max 3,000 chars; **max 10,000 total results**; **5 requests/second per account**;
**search responses include no rate-limit headers**.

**⚠️ Correction to the brief:** `/crm/v3/objects/contacts/search` is **not deprecated** — it is
still documented and functional under the legacy reference (scope
`crm.objects.contacts.read`). There is **no** "search-in-body" variant. The dated
`/crm/objects/2026-09/contacts/search` is simply the current-generation path.

### List / pagination

Query params (identical across objects): `after`, `archived`, `associations`, `limit`,
`properties`, `propertiesWithHistory`. **Max 100 records per page** (batch operations also
capped at 100). Response shape:
```json
{ "results": [ ... ], "paging": { "next": { "after": "33452", "link": "..." } } }
```
Paginate by passing `paging.next.after`. A requested-but-undefined property is silently
omitted; a defined property with no value returns `null`.

### Notes / engagements — current recommendation

**Use the notes object** (`POST /crm/objects/2026-09/notes`), not the legacy engagements API.

Legacy engagements v1 (`POST /engagements/v1/engagements`) still appears in the
[legacy API overview](https://developers.hubspot.com/docs/api-reference/legacy/overview.md) and
has a raw OpenAPI spec, but it has **no guide page**, **no confirmed OAuth scope**, and **no
formal deprecation notice**. Treat it as unmaintained and use per-activity objects
(notes/calls/emails/meetings/tasks) under `/crm/objects/2026-09/`.

### Associating a note (current form)

- `PUT /crm/objects/2026-09/notes/{noteId}/associations/{toObjectType}/{toObjectId}/{associationTypeId}`
  — e.g. `.../notes/17147287858/associations/contact/581751/202`
- `DELETE` on the same path removes it.
- Batch: `POST /crm/associations/2026-09/{fromObjectType}/{toObjectType}/batch/create`

**⚠️ Correction to the brief:** `PUT /crm/v4/objects/notes/{noteId}/associations/{toObjectType}/{toObjectId}`
(no trailing `associationTypeId`) is **not a documented form**. The v4 associations API does
exist (`/docs/specs/legacy/v4/crm-associations-v4.json`), but current guidance uses the
`2026-09` paths above. Batch limits: read ≤ 1,000 inputs; create ≤ 2,000.

---

## 2.8 Rate limits

Source: [Usage guidelines and limits](https://developers.hubspot.com/docs/developer-tooling/platform/usage-guidelines.md).

| App type | Per 10s | Per day |
|---|---|---|
| Privately distributed — Free/Starter | 100 / app | 250,000 / account |
| Privately distributed — Professional | 190 / app | 625,000 / account |
| Privately distributed — Enterprise | 190 / app | 1,000,000 / account |
| **Publicly distributed OAuth (Marketplace)** | **110 requests / 10s per installing account** | — |
| CRM Search API | **5 req/s per account** | — |

**⚠️ Correction to the brief:** the brief's "190/10s for OAuth apps" is **wrong**. 190/10s is
for **privately distributed** apps on Pro/Enterprise. Public OAuth apps get **110/10s per
installing account**. The API Limit Increase add-on does **not** raise the OAuth limit.

Exceeding limits → **429** with `policyName` of `DAILY` or `TEN_SECONDLY_ROLLING`. Headers:
`X-HubSpot-RateLimit-Daily`, `-Daily-Remaining`, `-Interval-Milliseconds`, `-Max`, `-Remaining`.
Daily headers are **not sent for OAuth-authorized requests**.

---

## 2.9 HubSpot — summary of corrections to the brief's premises

| Brief's premise | Reality |
|---|---|
| Use `/crm/v3/objects/...` | Current is **`/crm/objects/2026-09/...`**; `v3` is legacy |
| `/webhooks/v3/{appId}/subscriptions` is the webhooks API | It exists, but current is **`/app-webhooks/2026-09/{appId}/subscriptions`** |
| Request field `subscriptionType` | Request field is **`eventType`**; payload examples also use `eventType` |
| Payload has `sourceId`, `changeFlag` | **Not documented anywhere** — don't rely on them |
| `eventId` usable as a dedupe key | Docs: **"not guaranteed to be unique"** |
| `/crm/v3/objects/contacts/search` deprecated | **Not deprecated**; still documented |
| `PUT /crm/v4/objects/notes/{id}/associations/{type}/{id}` | Not a documented form; needs trailing `associationTypeId` |
| OAuth rate limit 190/10s | **110/10s per installing account** |
| HubSpot may not sign payloads like Atlassian | **HubSpot DOES sign** — v3 HMAC-SHA256, base64 |

## 2.10 HubSpot — explicitly UNCONFIRMED

1. **`eventType` vs `subscriptionType` in delivered payloads** — the official field table and the
   official JSON examples disagree. Decode defensively.
2. **Auth for subscription/settings endpoints** — OpenAPI says OAuth2 `developers-write` /
   `private-apps-write`; prose says Developer API key via `?hapikey`. Unresolved.
3. **Whether a private app's own OAuth token can manage its webhook subscriptions** — the
   private-app docs say API management is unsupported, implying no. Unverified.
4. **Deals path form** — `/crm/objects/2026-09/0-3` (OpenAPI) vs `/crm/objects/2026-09/deals`
   (prose). Test against a live account.
5. **`crm.objects.notes.read` / `.write`** — appear in some scope lists but not on the notes
   endpoints' own panels. Whether installable is unconfirmed.
6. **Scopes for `object.*` (custom objects) and `event.completed`** — present in the schema enum
   but absent from the prose scope table.
7. **Legacy engagements v1 OAuth scope** — not documented anywhere current.
8. **Whether `developers-write` / `private-apps-write` are selectable in the app config UI.**

---

# Cross-provider summary for the Go service

| Concern | Atlassian | HubSpot |
|---|---|---|
| OAuth2 flavour | 3LO authorization-code, rotating refresh (90-day inactivity) | OAuth2 authorization-code (user tokens) / client-credentials for journal APIs |
| API host | `api.atlassian.com/ex/{jira\|confluence}/{cloudId}` | `api.hubapi.com` |
| Tenant ID | `cloudId` from `/oauth/token/accessible-resources` | `portalId` (HubSpot account ID) |
| Webhook registration | `POST /rest/api/3/webhook` ✅ REST | Legacy public app: `/app-webhooks/2026-09/{appId}/subscriptions` ✅ · Private app: ❌ UI only · Project app: config file |
| Webhook URL per tenant? | One URL per app (shared across tenants) | One `targetUrl` per app; `portalId` in payload distinguishes tenants |
| **Body HMAC signature** | ❌ **No shared secret for dynamic webhooks** — verify bearer JWT instead | ✅ **HMAC-SHA256, base64**, over `method+uri+body+timestamp` |
| Replay window | none documented | **5 minutes** (v3) |
| Webhook expiry | **30 days — must refresh via `PUT /webhook/refresh`** | none |
| Registration limit | **5 per app per user per tenant** (OAuth2) | **1000 per app** |
| Batch size | 1 event per delivery | **up to 100 events per request** |
| Retries | 5 retries, 5–15 min backoff | **10 retries over 24 h** |
| Idempotency key | `X-Atlassian-Webhook-Identifier` + `timestamp` | **no unique key** (`eventId` explicitly non-unique) → composite key needed |
| JQL/search pagination | cursor `nextPageToken` (old `startAt` **removed**) | `paging.next.after` |
| Legacy search removed? | ✅ **`/rest/api/3/search` fully removed** → use `/rest/api/3/search/jql` | ❌ v3 search still works |

## Highest-risk items to resolve before implementation

1. **Atlassian dynamic webhooks cannot be HMAC-verified.** There is no shared secret. The only
   authenticity mechanism is the `Authorization: Bearer <token signed with client secret>`
   header, whose algorithm/claims Atlassian **does not document**. Budget time to capture a
   real delivery and reverse-engineer the claim set.
2. **Atlassian webhook URL constraint for OAuth2 apps** — the "same base URL as the app"
   wording is Connect-specific and its 3LO equivalent is undocumented. Confirm empirically that
   registration succeeds from your domain.
3. **Atlassian `/rest/api/3/search` is gone.** Any existing `startAt`-based client must be
   rewritten to `/rest/api/3/search/jql` with `nextPageToken`.
4. **HubSpot private apps cannot manage webhooks via API.** Choose the app type deliberately.
5. **HubSpot webhook subscription endpoint auth is contradictory** (OAuth scope vs `?hapikey`).
6. **Confluence Cloud has no dynamic webhook REST API.** If Confluence eventing is required,
   the design must change (Forge/Connect, or polling).
