# Notion Public REST API — Verified Reference for a Go Service

**Verification method:** all facts below were read from official Notion developer documentation, preferring the Mintlify `.md` variants (e.g. `…/reference/post-page.md`), the official `llms.txt` index (<https://developers.notion.com/llms.txt>), and the **full official OpenAPI 3.1 spec** downloaded from <https://developers.notion.com/openapi.json> (48 paths) plus <https://developers.notion.com/openapi-undocumented.json> (50 paths). Where prose and OpenAPI disagreed, both are reported.

**Three headline corrections to common assumptions**

1. The latest API version is **`2026-03-11`**, not `2025-09-03`.
2. Notion OAuth2 **does** use refresh tokens (`grant_type: "refresh_token"`). The old "Notion has no refresh tokens" belief is outdated.
3. There is **no REST API** to create/list/update/delete webhook subscriptions. Only the Developer-portal UI.

---

## 1. NOTION-VERSION HEADER

| Item | Value |
| :--- | :--- |
| Header name | `Notion-Version` |
| Exact current value | `2026-03-11` |
| Latest version source | <https://developers.notion.com/reference/versioning.md> (`export const latestApiVersion = <code>2026-03-11</code>`; "The most recent `Notion-Version` is 2026-03-11") |
| Required? | **Yes.** OpenAPI declares the parameter `required: true` on every operation. Versioning page: "Setting this header is **required**." |
| Omitted → | HTTP **400**, error code **`missing_version`**, message `"Notion-Version header failed validation: Notion-Version header should be defined, instead was undefined."` (<https://developers.notion.com/reference/status-codes.md>) |

**Is 2025-09-03 still valid?** Yes, it is a real, still-accepted version — it just is not the newest. Upgrading is not forced: "We don't currently have any plans to stop supporting older API versions." ([versioning](https://developers.notion.com/reference/versioning.md)). However the OpenAPI `notionVersion` parameter schema in the current spec enumerates **only** `["2026-03-11"]` — i.e. the published spec does not advertise older values even though the prose says they still work.

**Version history (from <https://developers.notion.com/reference/changes-by-version.md>)**

| Version | Breaking changes |
| :--- | :--- |
| `2026-03-11` | `after` → `position` object on Append block children; `archived` removed in favour of `in_trash` everywhere; `transcription` block type renamed to `meeting_notes` |
| `2025-09-03` | `/v1/databases` split into `/v1/data_sources` (individual tables) and `/v1/databases` (container) |
| `2022-06-28` | Deep page properties require the page-properties endpoint; parents are direct parents; relation `single_property`/`dual_property` |
| `2022-02-22`, `2021-08-16`, `2021-05-13` | (older) |

**2026-03-11 vs 2025-09-03 — the only three differences** (<https://developers.notion.com/guides/get-started/upgrade-guide-2026-03-11.md>):

| Change | On `2025-09-03` | On `2026-03-11` |
| :--- | :--- | :--- |
| Block positioning | `"after": "<block_id>"` | `"position": {"type":"after_block","after_block":{"id":"…"}}` (plus `start` / `end`) |
| Trash status | `"archived": true` (deprecated alias) | `"in_trash": true` — `archived` **fully removed** |
| Meeting notes | block type `transcription` | block type `meeting_notes` |

Webhook payloads are **identical** between `2025-09-03` and `2026-03-11` (the `archived → in_trash` rename applies only to REST bodies). Webhook subscriptions are versioned separately in the portal.

**Also available:** a second, optional header `Notion-Beta`, e.g. `Notion-Beta: notion-as-code-2026-07-31` (kebab-case name + revision date, comma-separated for multiple). Unknown/malformed → 400 `invalid_beta`. Not needed for anything in this report.

---

## 2. ENDPOINTS

Base URL for every call: `https://api.notion.com`. HTTPS required. Every request needs `Authorization` **and** `Notion-Version: 2026-03-11`.

All paths below were cross-checked against the OpenAPI `paths` object. The current spec contains **no** `/v1/databases/{database_id}/query` and **no** `/webhooks` path of any kind.

### `database` vs `data source` — the distinction (read this first)

Since `2025-09-03` the API model is (<https://developers.notion.com/guides/get-started/upgrade-faqs-2025-09-03.md>):

* **Database** = a *container*. `object: "database"`. Holds `data_sources[]`. Parent is typically `page_id` or `workspace`. Endpoints: `POST /v1/databases`, `GET|PATCH /v1/databases/{database_id}`. Manages container attributes: `title`, `description`, `icon`, `cover`, `is_inline`, `in_trash`, `parent`.
* **Data source** = a *table* with a schema (columns). `object: "data_source"`. Its `parent` is `{"type":"database_id", …}`; its `database_parent` is the grandparent. Endpoints: `POST /v1/data_sources`, `GET|PATCH /v1/data_sources/{data_source_id}`, `POST /v1/data_sources/{data_source_id}/query`. Manages `properties` (schema), `title`, `in_trash`.
* **Page (row)** lives under a **data source**: `parent: {"type":"data_source_id","data_source_id":"…"}`.

Permissions are **per database, not per data source**: "User and bot permissions are managed at the **database** level, not per data source." Sharing the container grants all its data sources.

Discovery flow for a Go client: `GET /v1/databases/{database_id}` → read `data_sources[].id` → use that id with `/v1/data_sources/...`.

**Does a `database_id` parent still work?** The `2026-03-11` OpenAPI for `POST /v1/pages` still enumerates four `parent` variants — `page_id`, **`database_id`**, `data_source_id`, `workspace` — so `database_id` is not rejected by the schema. The upgrade guide states that providing `data_source_id` "works for any API version", implying `database_id` likewise remained valid. **Use `data_source_id`.** Caveat: the docs never state explicitly how `database_id` resolves when the container has multiple data sources (2025-09-03 release notes only say older versions get `validation_error` `multiple_data_sources_for_database`). See §5.

Note the parent-variant schemas: only the id field is `required`; the `type` discriminator is optional in the schema for each variant.

---

### a. Create a page (data source parent)

**`POST /v1/pages`** — <https://developers.notion.com/reference/post-page.md>

Top-level body fields in the current spec: `parent`, `properties`, `icon`, `cover`, `content`, `children`, `markdown`, `allow_async`, `template`, `position`. (`content` and `children` are both `array` of `blockObjectRequest`, `maxItems: 100`; `markdown` is documented as "Mutually exclusive with content/children".) The body has **no** required fields in the schema; `children` when supplied is capped at 100.

Query parameter: `filter_properties` — array of property IDs; write response then contains only those properties (added 2026-08-12).

Minimal realistic body:

```json
{
  "parent": { "type": "data_source_id", "data_source_id": "d9824bdc-8445-4327-be8b-5b47500af6ce" },
  "properties": {
    "Name": { "type": "title", "title": [ { "type": "text", "text": { "content": "Tomatoes" } } ] },
    "Price": { "type": "number", "number": 1.49 },
    "Last ordered": { "type": "date", "date": { "start": "2026-01-15" } },
    "Tags": { "type": "multi_select", "multi_select": [ { "name": "grocery" } ] }
  },
  "children": [
    { "object": "block", "type": "paragraph",
      "paragraph": { "rich_text": [ { "type": "text", "text": { "content": "Buy kale." } } ] } }
  ]
}
```

Rules:

* `properties` keys must match the parent data source's schema (names or property IDs). Property values follow <https://developers.notion.com/reference/page-property-values.md>.
* Not settable via API (`validation_error`): `rollup`, `created_by`, `created_time`, `last_edited_by`, `last_edited_time`.
* `template` is `{"type":"none"}` (default) | `{"type":"default","timezone"?}` | `{"type":"template_id","template_id","timezone"?}`. When a template is used, `children` is **not allowed**; content is applied asynchronously after the response.
* `markdown` (string) replaces block content; with `"allow_async": true` the call returns HTTP **202** with an `async_task` object instead of a page.
* Optional `position` uses **`pagePositionSchema`**: `{"type":"after_block","after_block":{"id":"…"}}` | `{"type":"page_start"}` | `{"type":"page_end"}`. (Note: `page_start`/`page_end` here, *not* `start`/`end` as in the block-level `contentPositionSchema`.)
* `icon`/`cover` accept the usual emoji / external / file_upload / native-icon / custom-emoji shapes.

**Response:** `200` → a **page object** (`object: "page"`); or `202` + `async_task` when `markdown` + `allow_async`.

**Capability:** Insert content → otherwise **403**.

### b. Create a page under a parent page

**`POST /v1/pages`** (same endpoint)

```json
{
  "parent": { "type": "page_id", "page_id": "494c87d0-72c4-4cf6-960f-55f8427f7692" },
  "properties": {
    "title": { "type": "title", "title": [ { "type": "text", "text": { "content": "A note" } } ] }
  },
  "children": [
    { "object": "block", "type": "paragraph",
      "paragraph": { "rich_text": [ { "type": "text", "text": { "content": "Hello." } } ] } }
  ]
}
```

"If the new page is a child of an existing page, `title` is the only valid property in the `properties` body parameter." Response: page object.

Public connections and PATs may also create a private workspace-level page by **omitting** `parent` or sending `{"type":"workspace","workspace":true}`. Internal connections **cannot** — a page or data source parent is required (no owning user).

**Capability:** Insert content → 403 otherwise.

### c. Retrieve a page, and read its content

**`GET /v1/pages/{page_id}`** — <https://developers.notion.com/reference/retrieve-a-page.md>

Query parameter: `filter_properties` (array of property IDs, `maxItems: 100`).

**Response:** page object. Key fields: `object`, `id`, `created_time`, `last_edited_time`, `in_trash`, `is_archived`, `is_locked`, `url`, `public_url`, `parent`, `properties`, `icon`, `cover`, `created_by`, `last_edited_by`.

Limits: at most **25 references** per property (`people`, `relation`, `rich_text`, `title`); use `GET /v1/pages/{page_id}/properties/{property_id}` (Retrieve a page property item, cursor-paginated) for more. **404** if the page does not exist *or* is not shared with the connection.

**Content is not returned here.** Page content = block children:

**`GET /v1/blocks/{block_id}/children`** — pass the **page id as `block_id`**.

* Query params: `page_size` (default 100, max 100), `start_cursor` (opaque; pass back `next_cursor` verbatim).
* Returns **first level only**. Recurse for every block where `has_children: true`.
* Response: `{"object":"list","type":"block","block":{},"results":[block objects],"has_more":bool,"next_cursor":string|null}`.

**Capability:** Read content → 403 otherwise.

### d. Update page properties / trash a page

**`PATCH /v1/pages/{page_id}`** — <https://developers.notion.com/reference/patch-page.md>

Body fields in the current spec: `properties`, `icon`, `cover`, `is_locked`, `template`, `erase_content`, `in_trash`, `is_archived`. No required fields. Query param `filter_properties`.

```json
{
  "properties": {
    "Status": { "type": "status", "status": { "name": "In progress" } },
    "Due date": { "type": "date", "date": { "start": "2026-02-01", "end": null, "time_zone": null } }
  },
  "icon": { "type": "emoji", "emoji": "🐞" },
  "is_locked": false
}
```

Other documented bodies:

* Apply template: `{"template":{"type":"default"}}` or `{"template":{"type":"template_id","template_id":"…","timezone":"America/New_York"}}`.
* Clear all page content (destructive, irreversible via API): `{"erase_content": true}`.
* Content is added with **Append block children** (pass the page id as `block_id`), not this endpoint.

**Response:** the updated page object. Limitations: rollup values not updatable; a page's `parent` cannot be changed here (use `POST /v1/pages/{page_id}/move`).

**Capability:** Update content → 403 otherwise.

### e. Query a data source (filters + sorts)

**`POST /v1/data_sources/{data_source_id}/query`** — <https://developers.notion.com/reference/query-a-data-source.md>

> The old **`POST /v1/databases/{database_id}/query`** is **deprecated as of `2025-09-03`** and is **absent from the current OpenAPI spec**. See <https://developers.notion.com/reference/post-database-query.md> ("Deprecated as of version 2025-09-03 … Refer to the new APIs instead: Query a data source").

Body fields: `sorts` (array), `filter` (object), `start_cursor`, `page_size`, `is_archived` (bool), `result_type` (`"page"` | `"data_source"` — relevant for wiki data sources). No required fields. Query param `filter_properties` (property names or IDs; `?filter_properties[]=title&filter_properties[]=status`).

Concrete **compound** example (checkbox AND (select OR number-with-date-window)):

```json
{
  "filter": {
    "and": [
      { "property": "Done", "checkbox": { "equals": true } },
      {
        "or": [
          { "property": "Tags", "multi_select": { "contains": "A" } },
          { "property": "Owner", "select": { "equals": ["Alice", "Bob"] } }
        ]
      },
      { "property": "Due date", "date": { "on_or_after": "today" } }
    ]
  },
  "sorts": [
    { "property": "Priority", "direction": "descending" },
    { "timestamp": "last_edited_time", "direction": "ascending" }
  ],
  "page_size": 100,
  "start_cursor": "33e19cb9-751f-4993-b74d-234d67d0d534"
}
```

Compound rules: `and` / `or` take arrays of filter objects or nested compounds; **nesting is supported up to two levels deep**. A bare property filter is also valid (`{"property":"Done","checkbox":{"equals":true}}`).

**Timestamp filter** (no `property` key — supplying one is an error):

```json
{ "filter": { "timestamp": "created_time", "created_time": { "on_or_before": "2026-01-15" } } }
```

**Filter operators by property type** (<https://developers.notion.com/reference/filter-data-source-entries.md>):

| Filter field | Operators |
| :--- | :--- |
| `checkbox` | `equals`, `does_not_equal` |
| `date` | `after`, `before`, `equals`, `is_empty`, `is_not_empty`, `next_month`, `next_week`, `next_year`, `on_or_after`, `on_or_before`, `past_month`, `past_week`, `past_year`, `this_week` |
| `files` | `is_empty`, `is_not_empty` |
| `formula` | nested `checkbox` / `date` / `number` / `string` conditions |
| `multi_select` | `contains`, `does_not_contain`, `is_empty`, `is_not_empty` (string or `string[]`) |
| `number` | `does_not_equal`, `equals`, `greater_than`, `greater_than_or_equal_to`, `is_empty`, `is_not_empty`, `less_than`, `less_than_or_equal_to` |
| `people` (also applies to `created_by`, `last_edited_by`) | `contains`, `does_not_contain`, `is_empty`, `is_not_empty`; values are user UUIDs or `"me"` |
| `relation` | `contains`, `does_not_contain`, `is_empty`, `is_not_empty` |
| `rich_text` (also used for `title` and formula `string`) | `contains`, `does_not_contain`, `does_not_equal`, `ends_with`, `equals`, `is_empty`, `is_not_empty`, `starts_with` |
| `rollup` | `any` / `every` / `none` wrapping a condition; or `date` / `number` sub-conditions |
| `select` | `equals`, `does_not_equal`, `is_empty`, `is_not_empty` (string or `string[]`) |
| `status` | `equals`, `does_not_equal`, `is_empty`, `is_not_empty` (option names or group names like `"To-do"`, `"In progress"`, `"Complete"`) |
| `timestamp` | `created_time` or `last_edited_time`, each holding a `date` condition |
| `verification` | `status`, `does_not_equal` — values `verified`, `expired`, `none` |
| `unique_id` (field name in body is `unique_id`; the docs' table says "ID") | `equals`, `does_not_equal`, `greater_than`, `greater_than_or_equal_to`, `less_than`, `less_than_or_equal_to` |

Relative date values accepted by `after`, `before`, `equals`, `on_or_after`, `on_or_before`: `"today"`, `"tomorrow"`, `"yesterday"`, `"one_week_ago"`, `"one_week_from_now"`, `"one_month_ago"`, `"one_month_from_now"`.

`"me"` semantics: resolves to the authorizing user for public connections and to the token creator for PATs; for **internal connections there is no associated user**, so `contains: "me"` returns nothing and `does_not_contain: "me"` matches everything.

**Sorts** (<https://developers.notion.com/reference/sort-data-source-entries.md>): array, earlier entries take precedence. Either `{"property": "<name>", "direction": "ascending"|"descending"}` or `{"timestamp": "created_time"|"last_edited_time", "direction": …}`. No guaranteed order when `sorts` is omitted.

**Response:** a list object — `type: "page_or_data_source"`, `page_or_data_source: {}`, `object: "list"`, `results` (page objects and/or data source objects), `next_cursor`, `has_more`, and `request_status: {"type":"complete"|"incomplete","incomplete_reason":"query_result_limit_reached"}`.

* Archived rows: by default only non-archived pages are returned; set top-level `"is_archived": true` to query the archived partition instead (applied before filters/sorts). `in_trash` is **not** a supported query body parameter.
* Pagination caps at **10,000 results per query**; the capped response carries `request_status.type = "incomplete"`.
* **503** `service_unavailable` is possible when the backend datastore times out; the body's `additional_data.retry_guidance` advises exponential backoff, smaller `page_size`, narrower filters. Also **404** if not shared, **403** without read content.

### f. Retrieve a database (and a data source)

**`GET /v1/databases/{database_id}`** — <https://developers.notion.com/reference/retrieve-database.md>

No query params. **Response:** database object — `object: "database"`, `id`, `title`, `description`, `parent`, `is_inline`, `database_type` (`tasks|projects|sprints|docs|wiki|meetings|meeting_notes|skills|github_prs` or `null`), `in_trash`, `is_locked`, `created_time`, `last_edited_time`, **`data_sources: [{"id","name"}]`**, `icon`, `cover`, `url`, `public_url`. **404** if missing/unshared.

**`GET /v1/data_sources/{data_source_id}`** — <https://developers.notion.com/reference/retrieve-a-data-source.md>

**Response:** data source object — `object: "data_source"`, `id`, `title`, `description`, `parent` (`database_id` or, for externally synced sources, `data_source_id`), `database_parent`, `is_inline`, `database_type`, `in_trash`, `created_time`, `last_edited_time`, `created_by`, `last_edited_by`, **`properties`** (the schema), `icon`, `cover`, `url`, `public_url`.

Notes: a database ID is not interchangeable with a data source ID; related databases of relation properties must also be shared or those properties are omitted; linked data sources are not supported (share the source database).

### g. Append block children

**`PATCH /v1/blocks/{block_id}/children`** — <https://developers.notion.com/reference/patch-block-children.md>

Body (`children` required):

```json
{
  "children": [
    { "object": "block", "type": "paragraph",
      "paragraph": { "rich_text": [ { "type": "text", "text": { "content": "– Notion API Team" } } ] } }
  ],
  "position": { "type": "after_block", "after_block": { "id": "b5d8fd79-1234-1234-1234-123456789abc" } }
}
```

`position` is a one-of:

| Value | Meaning |
| :--- | :--- |
| `{"type":"end"}` | append at end (**default when `position` omitted**) |
| `{"type":"start"}` | insert at beginning |
| `{"type":"after_block","after_block":{"id":"<block_id>"}}` | insert after that block (replaces the old `after`) |

**Limits**

* **100 block children per request** ("Arrays of block children longer than 100 will result in an error"; schema `maxItems: 100`).
* **Up to 2 levels of nesting in a single request** ("For blocks that allow children, we allow up to **two** levels of nesting in a single request").
* Request payload ceiling: **1000 block elements and 500 KB** overall, and **100 elements** for any block/rich-text array (<https://developers.notion.com/reference/request-limits.md>).
* Existing blocks cannot be moved with this endpoint.

**`after` is deprecated**: you cannot send both `after` and `position`; on `2026-03-11` use `position` only.

**Response:** paginated list of the **newly created first-level children** — `type: "block"`, `block: {}`, `object: "list"`, `results`, `has_more`, `next_cursor`. **Capability:** Insert content → 403.

### h. Retrieve block children

**`GET /v1/blocks/{block_id}/children`** — <https://developers.notion.com/reference/get-block-children.md>

| Param | In | Notes |
| :--- | :--- | :--- |
| `block_id` | path | required — may be a page id |
| `page_size` | query | default **100**, maximum **100** |
| `start_cursor` | query | opaque `next_cursor` from the prior page |

**Response:** list object as in (c). First level only; recurse on `has_children: true`. **Capability:** Read content → 403.

Related: `GET /v1/blocks/{block_id}` retrieves a single block; `DELETE /v1/blocks/{block_id}` sets a block (including page blocks) to `in_trash: true` and requires **Update content** (<https://developers.notion.com/reference/delete-a-block.md>).

### i. Search

**`POST /v1/search`** — <https://developers.notion.com/reference/post-search.md>

Body fields: `query` (string), `sort`, `filter`, `start_cursor`, `page_size`. Nothing is required — with no `query`, all pages/data sources shared with the connection are returned.

```json
{
  "query": "meeting notes",
  "filter": { "property": "object", "value": "data_source" },
  "sort": { "timestamp": "last_edited_time", "direction": "descending" },
  "page_size": 100
}
```

* `filter` is one of `{"property":"object","value":"page"|"data_source","in_trash":bool}` (note: **`data_source`**, not `database`, since `2025-09-03`) or `{"in_trash": true}` alone.
* `sort` is one of `{"timestamp":"last_edited_time","direction":"ascending"|"descending"}` or `{"property":"relevance"}`.
* `page_size` default 100 / max 100; cursor-paginated.

**Response:** `{"object":"list","type":"page_or_data_source","page_or_data_source":{},"results":[page|data_source objects],"has_more":…,"next_cursor":…,"request_status":{…}}`.

Notes: excluding duplicated linked databases; matches titles; to search inside one data source use Query a data source instead. Older versions returned `database` objects here.

### j. Archive / delete a page

**There is no permanent-delete endpoint for pages.** <https://developers.notion.com/reference/trash-page.md>: "The API does not support permanently deleting pages."

**Trash (on `2026-03-11`):**

```
PATCH /v1/pages/{page_id}
Authorization: Bearer <token>
Notion-Version: 2026-03-11
Content-Type: application/json

{ "in_trash": true }
```

Restore with `{ "in_trash": false }`. Both return `200` + the page object.

**`archived` vs `in_trash` — verified:** "In API versions before `2026-03-11`, `archived` was a deprecated alias for `in_trash`. Starting with `2026-03-11`, **only `in_trash` is accepted**." It was deprecated in April 2024 (<https://developers.notion.com/page/changelog.md>) and removed in `2026-03-11`. So in a Go client targeting `2026-03-11`, send **`in_trash`** only; on `2025-09-03` `archived` still worked (same effect). The April-2024 changelog language "may be removed in a future API version" is now realised.

Separately, `PATCH /v1/pages/{page_id}` also accepts `is_archived` (boolean) and page objects returned by the API include both `in_trash` and `is_archived` (per the OpenAPI `pageObjectResponse`, where both are required). The query documentation explains the difference: "`is_archived` is separate from `in_trash`. Page objects include `is_archived` to indicate whether a page is archived, while `in_trash` indicates trash status and is used by page/block/data source update APIs." The prose Page-object table at <https://developers.notion.com/reference/page.md> is inconsistent with this (it lists `archived` as a deprecated alias of `in_trash` and omits `is_archived`). **Recommendation: use `in_trash` for trash/restore; treat `is_archived` as the archived-rows selector on queries. Do not send `archived` on `2026-03-11`.** See §5.

Blocks (including page blocks) can be trashed via `DELETE /v1/blocks/{block_id}`; databases/data sources use `in_trash` on their own update endpoints.

---

## 3. CAPABILITIES / PERMISSIONS

Source: <https://developers.notion.com/reference/capabilities.md>

**Content capabilities** (any combination allowed)

* **Read content** — read existing content. Example given: can call Retrieve a database, but not Update database.
* **Update content** — modify existing content. Example: can call Update page, but cannot create pages.
* **Insert content** — create new content; does **not** grant reading full objects. Example: can call Create a page, but cannot update existing pages.

**Comment capabilities** (off by default)

* **Read comments** — read comments on a page/block.
* **Insert comments** — insert comments on a page or existing discussion.

**User capabilities** (exact names as documented)

* **No user information** — no name, profile image, or email returned.
* **User information without email addresses** — name and profile image, no email.
* **User information with email addresses** — includes email.

(The changelog also mentions a newer **"View sessions and interact with agents"** capability for Custom Agent MCP tools — it is not listed on the capabilities reference page.)

**Endpoint → capability map**

| Action (endpoint) | Capability required (exact name) | Evidence |
| :--- | :--- | :--- |
| Create page — `POST /v1/pages` | **Insert content** | post-page.md info box. (The same box's final sentence says "without update content capabilities returns … 403" — an apparent documentation typo; the first sentence names Insert content.) |
| Create page under a page — `POST /v1/pages` | **Insert content** on the parent page | post-page.md |
| Retrieve page — `GET /v1/pages/{page_id}` | **Read content** | retrieve-a-page.md |
| Retrieve a page property item — `GET /v1/pages/{page_id}/properties/{property_id}` | **Read content** | reference/intro + page-property-values |
| Update page properties / icon / cover / lock / trash — `PATCH /v1/pages/{page_id}` | **Update content** | patch-page.md |
| Query a data source — `POST /v1/data_sources/{id}/query` | **Read content** | query-a-data-source.md |
| Retrieve a database — `GET /v1/databases/{id}` | **Read content** | capabilities.md (explicit example) |
| Retrieve a data source — `GET /v1/data_sources/{id}` | **Read content** *(inferred — the page only says the response "adheres to any limits to a connection's capabilities")* | retrieve-a-data-source.md |
| Retrieve block children — `GET /v1/blocks/{id}/children` | **Read content** | get-block-children.md |
| Retrieve a block — `GET /v1/blocks/{id}` | **Read content** *(by analogy; not stated on that page)* | — |
| Append block children — `PATCH /v1/blocks/{id}/children` | **Insert content** | patch-block-children.md |
| Update a block — `PATCH /v1/blocks/{id}` | **Update content** *(by analogy)* | — |
| Delete/trash a block — `DELETE /v1/blocks/{id}` | **Update content** | delete-a-block.md |
| Search — `POST /v1/search` | not stated per-endpoint; results "adhere to any limitations related to a connection's capabilities" → effectively **Read content** *(inferred)* | post-search.md |
| Create comment — `POST /v1/comments` | **Insert comments** | create-a-comment.md |
| List comments — `GET /v1/comments` | **Read comments** | capabilities.md |
| Retrieve your token's bot user — `GET /v1/users/me` | none — "accessible from by connections with any level of capabilities"; returned user object still obeys user capabilities | get-self.md |
| User objects in any response (names, avatars, emails) | **No user information** / **User information without email addresses** / **User information with email addresses** | capabilities.md |
| Receiving `comment.created` webhooks | comment-read capability (webhooks page calls it "comment read"; capabilities page calls it "Read comments") | webhooks.md |

**What happens when a capability is missing → HTTP 403.** Every endpoint's info box says a missing capability "will return an HTTP response with a 403 status code". The 403 body uses error code **`restricted_resource`** ("The token lacks permission, or the request exceeds a workspace block limit"), message example `"API token does not have access to this resource."` (The 403 schema also allows `status_change_not_allowed`, `workspace_credits_exhausted`, `agent_credit_limit_reached`.)

**Missing sharing is different → HTTP 404** `object_not_found`, e.g. `"Could not find database with ID: …. Make sure the relevant pages and databases are shared with your connection \"My Connection\"."` So in Go: **403 = capability, 404 = not shared/not found.**

**How to share**

* Internal connection: no access by default. Grant via Developer portal → connection → **Content access** → Edit access, or in Notion via the page's **•••** menu → **Connections** → **+ Add connection**. Access is inherited by child pages; it belongs to the connection (not the sharing user) and persists if that user leaves. (<https://developers.notion.com/guides/get-started/internal-connections.md>)
* Public connection: the OAuth page picker grants the chosen pages/databases at authorization time; only the authorizing user can interact.
* PAT: acts as its creator and **does not need** pages shared with a bot; it inherits that user's permissions.
* To query a data source, **its parent database must be shared** (else 404). Permissions are per database, not per data source.

---

## 4. AUTH

Source: <https://developers.notion.com/reference/authentication.md>, <https://developers.notion.com/guides/get-started/authorization.md>

**Header format for all REST calls (internal token, PAT, and OAuth2 access token are identical):**

```http
Authorization: Bearer <token>
Notion-Version: 2026-03-11
Content-Type: application/json
```

There is no distinct scheme for internal vs OAuth2 — all are bearer tokens. Notion attributes actions differently: internal connections act as their bot identity, public connections act for the authorizing user, PATs act as their creator.

**Token prefixes / conventions**

* Newly generated Notion API tokens use the **`ntn_`** prefix; this changed on **2024-09-25** from **`secret_`**. Existing `secret_` tokens continue to work (<https://developers.notion.com/page/changelog.md>, "September 11, 2024").
* The secure-tokens guide shows `NOTION_API_KEY=ntn_abc123…` for what it describes as a token for internal connections, OAuth access tokens, and PATs: "This guidance applies to internal connection tokens, OAuth access tokens, and personal access tokens." (<https://developers.notion.com/guides/get-started/handling-api-keys.md>)
* Refresh tokens are shown with an **`nrt_`** prefix in the authorization guide's example (`"refresh_token":"nrt_4991090011501Ejc6Xn4sHguI7jZIN449mKe9PRhpMfNK"`).
* Webhook `verification_token` examples use the legacy `secret_` prefix in docs — do not rely on prefixes.
* **Notion explicitly advises against validating tokens with regex**: "The token format may change over time… treat the token as an opaque string."

**OAuth2 endpoints (public connections only)**

| Purpose | Method + path | Auth |
| :--- | :--- | :--- |
| Authorization URL (browser redirect) | `https://api.notion.com/v1/oauth/authorize?owner=user&client_id=…&redirect_uri=…&response_type=code&state=…` | none (`owner` must be `user`, `response_type` must be `code`) |
| Exchange code / refresh | **`POST https://api.notion.com/v1/oauth/token`** | **HTTP Basic**: `Authorization: Basic base64(CLIENT_ID:CLIENT_SECRET)` — *not* Bearer |
| Introspect token | `POST /v1/oauth/introspect` | (present in current OpenAPI) |
| Revoke token | `POST /v1/oauth/revoke` | (present in current OpenAPI) |

Exchange body: `{"grant_type":"authorization_code","code":"…","redirect_uri":"…"}` (`redirect_uri` required if it was sent in the authorize URL or if the connection has more than one registered redirect URI; **not allowed** otherwise). Optional `external_account: {key, name}` also exists in the schema.

Response fields: `access_token`, `token_type` (`"bearer"`), **`refresh_token`**, `bot_id`, `workspace_id`, `workspace_name`, `workspace_icon`, `owner`, `duplicated_template_id`, `request_id`.

**Refresh tokens — verified: Notion OAuth2 DOES use them.** The response schema and the authorization guide both document a required `refresh_token`, and there is a dedicated reference page <https://developers.notion.com/reference/refresh-a-token.md> ("Refreshes an access token, generating a new access token and new refresh token"). Refresh request:

```json
POST /v1/oauth/token
Authorization: Basic base64(CLIENT_ID:CLIENT_SECRET)
Content-Type: application/json

{ "grant_type": "refresh_token", "refresh_token": "nrt_…" }
```

Refreshing issues **both** a new access token and a new refresh token — store the new pair. The guide also warns to store the token pair from every successful authorization, including re-authorization.

OAuth error codes: `invalid_request`, `invalid_grant`, `unauthorized_client`, `unsupported_grant_type`, `invalid_scope` (400); `invalid_client` (401); `test_env_error` (403). Notion uses RFC 6749 error semantics.

**PATs**: created in the Developer portal, expire after 7/30/90/180 days or 1 year (default 1 year), can be revoked; the Notion API capability on a PAT is gated by a workspace policy; `GET /v1/users` (List all users) is **not** available to PATs — use `GET /v1/users/me`.

**Rate limits** (<https://developers.notion.com/reference/request-limits.md>): per-connection fixed 60-second window — **600 req/min** on Business/Enterprise, **180 req/min** on all other plans; plus a separate per-workspace shared limit. A 429 body carries `additional_data.rate_limit_reason` and `additional_data.retry_after` (string integer seconds); honour the `Retry-After` header. 529 `service_overload` should be retried like 429. The docs include a **Go** retry example (max 6 attempts, jittered backoff, retry 429/529 always and 500/502/503/504 only for idempotent GET/DELETE).

---

## 4b. WEBHOOKS — is there a REST API for subscriptions? **No.**

**Definitive answer: there is no public REST endpoint to create, list, update, verify, or delete webhook subscriptions. Subscriptions are dashboard-only.**

Evidence:

1. The complete official OpenAPI spec (<https://developers.notion.com/openapi.json>, 48 paths) contains **zero** paths matching `webhook`; the tag list has no Webhooks tag (Agent Skills, Databases, Data sources, Pages, Async tasks, Blocks, Comments, File uploads, OAuth, Users, Search, Views, Agents, Sessions, Custom emojis, Meeting notes).
2. The undocumented spec (<https://developers.notion.com/openapi-undocumented.json>, 50 paths) likewise contains **zero** webhook paths.
3. The docs describe creation purely as a UI flow: connection settings → **Webhooks** tab → **+ Create a subscription** → enter public HTTPS webhook URL → choose event types → **Create subscription** → receive one-time `verification_token` → paste it into the portal to verify (<https://developers.notion.com/reference/webhooks.md>). The pages under `/reference/webhooks/*` are **inbound event payload schemas**, not endpoints.
4. Subscription API version is chosen in the portal ("Edit subscription" → API version dropdown), and "You can only change the webhook URL before verification. After verification, if you need to change the URL, you must delete and recreate the subscription." The changelog says webhook automation upgrades are done via an "Upgrade to latest version" button in the UI.

So a Go service **receives** webhooks and must verify them:

* Header `X-Notion-Signature`, format `sha256=<hex hmac>`, where the HMAC-SHA256 key is the subscription's `verification_token` and the message is the **raw request body** (re-serialising JSON changes the bytes and fails; `<https://developers.notion.com/reference/webhooks.md>` provides JS/Python/Ruby samples — no Go sample). Compare in constant time.
* Delivery: at-most-once intent, retried up to **8** times with exponential backoff, final attempt ≈24 h after trigger; events should arrive within 5 minutes (usually <1 min).
* Event envelope fields: `id`, `timestamp`, `workspace_id`, `workspace_name`, `subscription_id`, `integration_id`, `type`, `authors[]`, `accessible_by[]` (public connections only), `attempt_number` (1–8), **`api_version`** (enum `2022-06-28` | `2025-09-03` | `2026-03-11`), `entity{id,type}`, `data`.
* Event types listed in the docs table: `page.content_updated`, `page.created`, `page.deleted`, `page.locked`, `page.moved`, `page.properties_updated`, `page.undeleted`, `page.unlocked`, `database.content_updated` (deprecated in 2025-09-03), `database.created`, `database.deleted`, `database.moved`, `database.schema_updated` (deprecated in 2025-09-03), `database.undeleted`, `data_source.content_updated|created|deleted|moved|schema_updated|undeleted` (new in 2025-09-03), `comment.created`, `comment.deleted`, `comment.updated`.
* ⚠️ The changelog (2026-03-19) says `view.created`, `view.updated`, `view.deleted` webhook events are "available on API version `2025-09-03` and later", and the docs index lists file-upload webhook event pages (`file-upload-created`, `file-upload-completed`, `file-upload-expired`, `file-upload-failed`) plus a `page.transcription_deleted` / transcript-deletion event — but these do **not** appear in the event table on <https://developers.notion.com/reference/webhooks-events-delivery.md>. Treat the table as incomplete and fetch the individual `/reference/webhooks/…` pages for exact payload schemas.

---

## 5. THINGS I COULD **NOT** CONFIRM (explicit list)

1. **`database_id` as a page parent on `2026-03-11` with a multi-source database.** The OpenAPI still lists the `database_id` parent variant and only requires the id, but no page states how it resolves when the container has several data sources. The multi-source `validation_error` text quoted in the FAQ is framed around pre-`2025-09-03` versions. **Mitigation: always send `data_source_id`.**
2. **`is_archived` semantics and write behaviour.** `PATCH /v1/pages/{page_id}` accepts `is_archived` and page objects include it, and the query doc distinguishes it from `in_trash` ("archived pages" partition vs trash). But no page explains what setting `is_archived` on a write does, and the Page-object prose table omits it while listing `archived` as a deprecated `in_trash` alias. Docs conflict internally; unverified.
3. **Search's response `type` string.** The OpenAPI says `const "page_or_data_source"`, but the pagination table in <https://developers.notion.com/reference/intro.md> lists `"page_or_database"` among possible `type` values. Unresolved doc inconsistency; parse defensively.
4. **Search's required capability.** No info box on the Search page names one; I inferred Read content.
5. **Capability for `GET /v1/data_sources/{id}`, `GET /v1/blocks/{id}`, `PATCH /v1/blocks/{id}`.** Not stated per-endpoint; inferred by analogy from the documented ones.
6. **Whether older `Notion-Version` values are still accepted at runtime.** Prose says yes; the current OpenAPI enum lists only `2026-03-11`. Not empirically tested here (no live token).
7. **A complete, authoritative capability list.** `reference/capabilities.md` documents 8 capability names, but the changelog references an additional `"View sessions and interact with agents"` capability not present on that page, and webhooks.md uses the wording "comment read" where the capabilities page says "Read comments". Wording is not fully consistent.
8. **The exact default/max `page_size` for `POST /v1/search` and `POST /v1/data_sources/…/query` as integers in the endpoint schema.** The OpenAPI declares `page_size` as an unconstrained `number`; the 100 default / 100 max figures come from <https://developers.notion.com/reference/intro.md>.
9. **`POST /v1/pages` `content` vs `children`.** Both are `array<blockObjectRequest>` with `maxItems: 100`, and `markdown` is documented as mutually exclusive with "content/children", but no doc explains whether `content` is a preferred alias or legacy name.
10. **`GET /v1/users` availability to internal connections vs PATs** beyond the explicit statement that PATs cannot use List all users.
11. **Go-specific verification code** — official docs ship signature-verification samples only in JavaScript, Python and Ruby; the Go snippet on the request-limits page is about retries, not webhook HMACs.
12. **Live behaviour of anything** — every statement here is from documentation and the published OpenAPI spec, not from executed requests against a real workspace.

---

## Canonical URL list

* Versioning — <https://developers.notion.com/reference/versioning.md>
* Changes by version — <https://developers.notion.com/reference/changes-by-version.md>
* Changelog — <https://developers.notion.com/page/changelog.md>
* Upgrade guide 2026-03-11 — <https://developers.notion.com/guides/get-started/upgrade-guide-2026-03-11.md>
* Upgrade guide 2025-09-03 — <https://developers.notion.com/guides/get-started/upgrade-guide-2025-09-03.md>
* Upgrade FAQs 2025-09-03 — <https://developers.notion.com/guides/get-started/upgrade-faqs-2025-09-03.md>
* Intro (conventions, pagination) — <https://developers.notion.com/reference/intro.md>
* Authentication — <https://developers.notion.com/reference/authentication.md>
* Authorization (OAuth flow) — <https://developers.notion.com/guides/get-started/authorization.md>
* Create a token — <https://developers.notion.com/reference/create-a-token.md>
* Refresh a token — <https://developers.notion.com/reference/refresh-a-token.md>
* Capabilities — <https://developers.notion.com/reference/capabilities.md>
* Status codes — <https://developers.notion.com/reference/status-codes.md>
* Request limits — <https://developers.notion.com/reference/request-limits.md>
* Create a page — <https://developers.notion.com/reference/post-page.md>
* Retrieve a page — <https://developers.notion.com/reference/retrieve-a-page.md>
* Update page — <https://developers.notion.com/reference/patch-page.md>
* Trash a page — <https://developers.notion.com/reference/trash-page.md>
* Page object — <https://developers.notion.com/reference/page.md>
* Append block children — <https://developers.notion.com/reference/patch-block-children.md>
* Retrieve block children — <https://developers.notion.com/reference/get-block-children.md>
* Delete a block — <https://developers.notion.com/reference/delete-a-block.md>
* Query a data source — <https://developers.notion.com/reference/query-a-data-source.md>
* Filter data source entries — <https://developers.notion.com/reference/filter-data-source-entries.md>
* Sort data source entries — <https://developers.notion.com/reference/sort-data-source-entries.md>
* Post database query (deprecated) — <https://developers.notion.com/reference/post-database-query.md>
* Retrieve a database — <https://developers.notion.com/reference/retrieve-database.md>
* Retrieve a data source — <https://developers.notion.com/reference/retrieve-a-data-source.md>
* Search — <https://developers.notion.com/reference/post-search.md>
* Working with page content — <https://developers.notion.com/guides/data-apis/working-with-page-content.md>
* Working with databases — <https://developers.notion.com/guides/data-apis/working-with-databases.md>
* Webhooks — <https://developers.notion.com/reference/webhooks.md>
* Event types & delivery — <https://developers.notion.com/reference/webhooks-events-delivery.md>
* OpenAPI (full) — <https://developers.notion.com/openapi.json>
* Docs index — <https://developers.notion.com/llms.txt>
