# Google Drive & Notion Public APIs — Research Report (2026)

Scope: a Go service that (a) registers webhooks/subscriptions and (b) calls REST actions with an
OAuth2 access token. Every endpoint path, header name, parameter and scope string below was taken
from official documentation (`developers.google.com`, `developers.notion.com`) or the providers'
own machine-readable specs. Anything I could not confirm is listed explicitly in
[§3 Unconfirmed](#3-unconfirmed--explicitly-flagged).

**Primary sources used**

| Source | Why it is authoritative |
| --- | --- |
| `https://www.googleapis.com/discovery/v1/apis/drive/v3/rest` | Google's own Discovery document — `revision: 20260904`. Machine-readable truth for paths, params, bodies, scopes. |
| `https://developers.notion.com/openapi.json` | Notion's full OpenAPI spec (48 paths) + `openapi-undocumented.json` (50 paths). |
| Provider guides & reference pages | Prose semantics, limits, security model. |

---

## 0. Executive summary — the five things that change your design

1. **Google Drive push notifications cannot be cryptographically verified.** There is **no HMAC
   signature** and no shared secret on the delivery. The only controls are (a) HTTPS, (b) an
   unguessable `token` you supply, echoed back verbatim in `X-Goog-Channel-Token`. A receiver can
   only do a constant-time string comparison. If your service needs provable authenticity of a
   delivery, Drive `watch` cannot provide it — use the Google Workspace Events API + Pub/Sub
   instead (§1.7), or treat a notification purely as a *hint* and re-fetch authoritative state
   through the authenticated Drive API.
2. **Notion webhooks DO exist in 2026, but there is no API to create them.** Subscription CRUD is
   dashboard-only. There is **no** `/v1/webhooks` path in Notion's OpenAPI spec. Your Go service is
   a *receiver* only; a human must create and verify the subscription in the Developer portal.
3. **Notion's current API version is `2026-03-11`, not `2025-09-03`.** `2025-09-03` still works.
   On `2026-03-11`, `archived` is **removed** (use `in_trash`) and `after` is replaced by a
   `position` object.
4. **Notion OAuth2 does use refresh tokens** (`nrt_…`), contrary to a common assumption. Notion
   issues a *new* refresh token on every refresh — you must persist the new pair.
5. **Drive `watch` channels never auto-renew and the docs publish no maximum TTL.** You must
   re-`watch` on a timer. A channel's real expiry is only discoverable from the `watch` response /
   `X-Goog-Channel-Expiration` header.

---

# Part 1 — Google Drive (and Google Workspace)

## 1.1 Push notifications: `changes.watch` and `files.watch`

Both are `POST` methods whose request **and** response body is a `Channel` resource.

### Exact endpoints

| Method | Endpoint |
| --- | --- |
| `changes.watch` | `POST https://www.googleapis.com/drive/v3/changes/watch` |
| `files.watch` | `POST https://www.googleapis.com/drive/v3/files/{fileId}/watch` |
| `channels.stop` | `POST https://www.googleapis.com/drive/v3/channels/stop` |

Guide: <https://developers.google.com/workspace/drive/api/guides/push>
Reference: <https://developers.google.com/workspace/drive/api/reference/rest/v3/changes/watch> ·
<https://developers.google.com/workspace/drive/api/reference/rest/v3/files/watch>

`Authorization: Bearer <ACCESS_TOKEN>` and `Content-Type: application/json` are required.

### Request body — `Channel` fields

| Field | Required | Meaning |
| --- | --- | --- |
| `id` | **Yes** | Unique channel ID within your project. Docs recommend a UUID. **Max 64 chars.** Echoed back as `X-Goog-Channel-ID`. Must be **unique per new channel** — reuse on renewal is not allowed. |
| `type` | **Yes** | Must be `"web_hook"`. (Discovery also lists `"webhook"` as a valid value; the guide uses `web_hook`.) |
| `address` | **Yes** | Your webhook callback URL. **Must be HTTPS** with a valid CA-signed cert. Self-signed, untrusted, revoked, or hostname-mismatched certs all fail. |
| `token` | No | Arbitrary string, **max 256 chars**. Echoed verbatim in `X-Goog-Channel-Token`. Docs recommend an extensible format like URL query params (`forwardTo=hr&createdBy=mobile`) and explicitly say **do not put OAuth tokens or sensitive data in it**. |
| `expiration` | No | Requested expiry as a **Unix timestamp in milliseconds**. Advisory — see §1.2 TTL. |
| `params` | No | Additional delivery params (map of string→string). Not needed for `web_hook`. |
| `payload` | No | Boolean, whether a payload is wanted. Drive notifications are body-less regardless (§1.3). |

Example:

```json
{
  "id": "4ba78bf0-6a47-11e2-bcfd-0800200c9a77",
  "type": "web_hook",
  "address": "https://mydomain.com/notifications",
  "token": "target=myApp-myChangesChannelDest",
  "expiration": 1426325213000
}
```

### `changes.watch` query parameters

`pageToken` is the important one. The Reference page's parameter table lists it as an ordinary
query param, but the official guide states plainly: *"While not documented in `changes.watch`, the
method does require a `pageToken` parameter similar to `changes.list`. This can be retrieved from
`changes.getStartPageToken`."* **Treat `pageToken` as required** and source it from
`changes.getStartPageToken`.

Other params: `driveId`, `includeCorpusRemovals`, `includeItemsFromAllDrives`, `includeRemoved`,
`pageSize` (max 1000), `restrictToMyDrive`, `spaces` (`drive`, `appDataFolder`),
`supportsAllDrives`, `includePermissionsForView`, `includeLabels`.
Deprecated: `includeTeamDriveItems`, `supportsTeamDrives`, `teamDriveId`.

`files.watch` query params: `supportsAllDrives`, `acknowledgeAbuse`, `includePermissionsForView`,
`includeLabels`. Path param: `fileId`.

### Response

HTTP `200 OK` with a `Channel`:

```json
{
  "kind": "api#channel",
  "id": "01234567-89ab-cdef-0123456789ab",
  "resourceId": "o3hgv1538sdjfh",
  "resourceUri": "https://www.googleapis.com/drive/v3/files/o3hgv1538sdjfh",
  "token": "target=myApp-myFilesChannelDest",
  "expiration": 1426325213000
}
```

**Persist `resourceId` and `expiration`** — `resourceId` is required by `channels.stop`, and
`expiration` is the only reliable source of the real TTL.

### Expiration / renewal

- "A notification channel can have an expiration time, with a value determined either by your
  request or by any Google Drive API internal limits or defaults (**the more restrictive value is
  used**)."
- "**Currently, there's no automatic way to renew a notification channel.**" You must call `watch`
  again with a **new unique `id`**. Docs note an expected "overlap" window where both channels are
  live.
- The real expiry is returned as a Unix-ms timestamp in the `watch` response and as a
  **human-readable** date in the `X-Goog-Channel-Expiration` header on every notification.
- ⚠️ **Maximum TTL is NOT published in Google's official documentation.** I could not find any
  official statement of a max or default TTL on the push guide, the `changes.watch` reference, or
  the Drive limits page. Community reports consistently cite **~24 hours** as the effective maximum
  (e.g. [Stack Overflow: "Google Drive API watch channel is only 24h"](https://stackoverflow.com/questions/70673398)),
  but this is **not** an official guarantee. **Design your renewal loop to re-`watch` well inside
  24 h (e.g. every 6–12 h) and to re-`watch` immediately whenever a `watch` response reports a
  nearer expiry.**

### Stopping a channel

`POST https://www.googleapis.com/drive/v3/channels/stop` with at least `id` and `resourceId`:

```json
{ "id": "4ba78bf0-6a47-11e2-bcfd-0800200c9a77", "resourceId": "ret08u3rv24htgh289g" }
```

Only users with the right permission can stop a channel: if created by a regular user account, only
the same user from the same OAuth client can stop it; if created by a service account, any user from
the same client can.

## 1.2 CRITICAL — how is a notification authenticated?

**Short answer: it isn't. There is no signature, no HMAC, no shared secret, and no timestamp.**

This is the single most important finding for a service that must verify deliveries. Google's push
guide describes `token` as follows (verbatim):

> A `token` property that specifies an arbitrary string value to use as a channel token. You can use
> notification channel tokens for various purposes. For example, **you can use the token to verify
> that each incoming message is for a channel that your application created—to ensure that the
> notification is not being spoofed**—or to route the message to the right destination within your
> application based on the purpose of this channel.

So the entire security model is: **unguessable channel token (≤256 chars) + HTTPS transport**.

### What a receiver CAN verify

| Check | Possible? | Notes |
| --- | --- | --- |
| `X-Goog-Channel-Token` equals the value you generated | ✅ | Constant-time compare. This is a **bearer-style shared secret**, not a signature. Anyone who learns the token can forge a delivery. |
| `X-Goog-Channel-ID` matches a channel you registered | ✅ | Also not secret in itself. |
| `X-Goog-Resource-ID` / `X-Goog-Resource-URI` match the stored `resourceId`/`resourceUri` from the `watch` response | ✅ | Good correlation/hygiene check. |
| Sender IP is in a Google-published range | ❌ | Google publishes **no** source-IP list for Drive push notifications. |
| A cryptographic signature proving Google sent it | ❌ | **Does not exist.** |
| Freshness / replay protection | ❌ | No timestamp header and no signed nonce. `X-Goog-Message-Number` is monotonic but **unauthenticated**, so it cannot be trusted for replay defence. |

### Recommended handling given the above

- Generate `token` from a CSPRNG (≥128 bits of entropy), store it per channel, compare in constant
  time. Reject on mismatch.
- **Do not treat the notification body as data** — it has none (§1.3) — and do not treat the
  notification as authoritative. Use it only as a signal to call the authenticated Drive API
  (`changes.list`) to learn what actually changed.
- Apply strict rate limiting and idempotency on the webhook endpoint; because delivery is
  unauthenticated, the endpoint is effectively an unauthenticated trigger.
- Corroborate the third-party evidence: Nango's advisory
  [GHSA-42f5-jg2r-v8j7](https://github.com/NangoHQ/nango/security/advisories/GHSA-42f5-jg2r-v8j7) is
  titled *"Unverified Google push notifications are forwarded to customer webhook endpoints with a
  valid Nango signature"* — i.e. an integration platform was burned by exactly this gap.

## 1.3 Notification delivery — exact headers

Delivered as `POST` to your `address`. **The request body is always empty for both `files` and
`changes` notifications** (`Content-Length: 0`).

```
POST https://mydomain.com/notifications
Content-Type: application/json; utf-8
Content-Length: 0
X-Goog-Channel-ID: 4ba78bf0-6a47-11e2-bcfd-0800200c9a66
X-Goog-Channel-Token: 3a98f1a2b3c4d5e6f7
X-Goog-Channel-Expiration: Tue, 19 Nov 2013 01:13:52 GMT
X-Goog-Resource-ID: ret08u3rv24htgh289g
X-Goog-Resource-URI: https://www.googleapis.com/drive/v3/files/ret08u3rv24htgh289g
X-Goog-Resource-State: update
X-Goog-Changed: content,properties
X-Goog-Message-Number: 11
```

| Header | Notes |
| --- | --- |
| `X-Goog-Channel-ID` | The `id` you supplied. |
| `X-Goog-Channel-Token` | The `token` you supplied. **Present only if you set one.** |
| `X-Goog-Channel-Expiration` | Human-readable date. Present only if the channel expires. |
| `X-Goog-Resource-ID` | Opaque, stable across API versions. |
| `X-Goog-Resource-URI` | Version-specific URI of the watched resource. |
| `X-Goog-Resource-State` | Event type — see §1.4. |
| `X-Goog-Changed` | **Only on `update`** for `files`. Comma-separated subset of `content`, `properties`, `parents`, `children`, `permissions`. |
| `X-Goog-Message-Number` | Monotonic per channel but **not sequential**. The `sync` message is always `1`; later messages are larger. Unauthenticated — do not use for replay defence. |

### Responding

Return `200`, `201`, `202`, `204`, or `102` to acknowledge. Per the guide, `500`, `502`, `503`,
`504` trigger retries with exponential backoff; **every other status code is treated as a message
failure** (i.e. no retry). Acknowledge fast and process asynchronously.

## 1.4 Resource states

| `X-Goog-Resource-State` | Applies to | Delivered when |
| --- | --- | --- |
| `sync` | `files`, `changes` | Channel successfully created. **May arrive before the `watch` HTTP response returns** — safe to ignore, or use for init. |
| `add` | `files` | Resource created or shared. |
| `remove` | `files` | Resource deleted or unshared. |
| `update` | `files` | One or more metadata properties updated. |
| `trash` | `files` | Resource moved to trash. |
| `untrash` | `files` | Resource removed from trash. |
| `change` | `changes` | One or more changelog items added. |

`X-Goog-Changed` values (on `files` `update`): `content`, `properties`, `parents`, `children`,
`permissions`.

Note the asymmetry: `files` gives granular states, `changes` gives only `change` — for `changes.watch`
you must call `changes.list` to discover what happened.

## 1.5 Change tracking — `getStartPageToken` and `changes.list`

Guide: <https://developers.google.com/workspace/drive/api/guides/manage-changes>

### Bootstrap

`GET https://www.googleapis.com/drive/v3/changes/getStartPageToken`

Returns `{"startPageToken": "..."}`. Store it. Optionally pass `driveId` and
`supportsAllDrives` to get a token for a shared drive's change log.

### Poll

`GET https://www.googleapis.com/drive/v3/changes/list?pageToken=<token>`

- Entries are **chronological, oldest first**.
- Response `ChangeList`: `changes[]`, `nextPageToken`, `newStartPageToken`, `kind`.
- Loop while `nextPageToken` is present; it can be passed straight back as `pageToken`.
- When `nextPageToken` is absent, **store `newStartPageToken`** for the next poll.
- **Page tokens do not expire** (per the `ChangeList` schema descriptions).
- Key params: `includeRemoved`, `restrictToMyDrive`, `includeCorpusRemovals`, `spaces`,
  `driveId`, `includeItemsFromAllDrives`, `supportsAllDrives`, `pageSize` (default 100, max 1000),
  `includeLabels`, `includePermissionsForView`.

### Semantics that will bite you

- A change entry is **the current state of the file, not a delta**. To know *what* changed you must
  persist the previous state and diff it yourself.
- Entries can be **invalidated and replaced** by a newer entry for the same item.
- **Tombstones**: entries for items no longer available are marked `deleted` and carry **only the
  item ID**. Crucially, this *"does not distinguish between loss of access and deletion"*, and a
  corpus move may also appear as a delete — use `includeCorpusRemovals` to disambiguate.
- Per-user and per-shared-drive change logs are **separate**. To track everything visible to a user
  you must replay the user log **and** every shared drive log they belong to. Changes to items
  inside a shared drive may never appear in the user's log.

## 1.6 Actions — exact REST calls, bodies and scopes

Base: `https://www.googleapis.com/drive/v3/` · Upload base: `https://www.googleapis.com/upload/drive/v3/files`

### (a) Upload / create a file

`POST https://www.googleapis.com/upload/drive/v3/files?uploadType=<media|multipart|resumable>`
Metadata-only: `POST https://www.googleapis.com/drive/v3/files`

| Mode | Limit | Notes |
| --- | --- | --- |
| `uploadType=media` | **≤ 5 MB** | Media only, no metadata. Set `Content-Type` to the object's MIME type. |
| `uploadType=multipart` | **≤ 5 MB** | `multipart/related`; **metadata part first**, media part second. |
| `uploadType=resumable` | up to **5 TiB** | Required > 5 MB. Returns `Location` = session URI, **valid ~1 week**. `PUT` bytes in 256 KB-multiple chunks. `308` = resume; `404` = session expired, restart. |

Resumable resume: send an empty `PUT` with `Content-Range: bytes */<total>`, read the `Range`
response header, continue from there. Initial request uses `PATCH` instead of `POST` when updating
an existing file.

**Creating Google Docs native types — this is the "conversion" mechanism.** You do *not* call a
convert endpoint; you upload the source bytes (`.docx`, `.md`, `.html`, …) and set the metadata
`mimeType` to the **Google Workspace** type. Discovery `File.mimeType` states: *"If a file is
created with a Google Doc MIME type, the uploaded content is imported, if possible."*

| Native type | `mimeType` |
| --- | --- |
| Google Doc | `application/vnd.google-apps.document` |
| Google Sheet | `application/vnd.google-apps.spreadsheet` |
| Google Slides | `application/vnd.google-apps.presentation` |
| Folder | `application/vnd.google-apps.folder` |
| Shortcut | `application/vnd.google-apps.shortcut` |

Supported import sources include Word/ODT/HTML/RTF/plain text/**Markdown** → Docs;
Excel/ODS/CSV/TSV → Sheets; PowerPoint/ODP → Slides. **Query `about.importFormats` at runtime** for
the authoritative live list. On **update**, the full document contents are replaced.

Folder creation:

```json
{ "name": "Reports", "mimeType": "application/vnd.google-apps.folder", "parents": ["FOLDER_ID"] }
```

The alias `"root"` may be used for the My Drive root anywhere a file ID is expected. **A file has
exactly one parent** — `parents` is effectively single-valued.

Writable `File` metadata fields: `name`, `mimeType`, `parents[]`, `description`, `starred`,
`trashed`, `properties` (public to all apps), `appProperties` (private to your app),
`contentHints`, `contentRestrictions`, `copyRequiresWriterPermission`, `writersCanShare`,
`folderColorRgb`, `originalFilename`, `shortcutDetails.targetId`, `labelInfo`, `id`, `createdTime`,
`modifiedTime`.

Pre-generated IDs: `GET https://www.googleapis.com/drive/v3/files/generateIds?count=10&space=drive`
(`count` max 1000). Not supported for Google Workspace native types except `.drive-sdk` and
`.folder`.

**Scopes (`files.create`):** `drive`, `drive.appdata`, `drive.file`.

### (b) Get file metadata

`GET https://www.googleapis.com/drive/v3/files/{fileId}?fields=...&supportsAllDrives=true`

Params: `fields`, `supportsAllDrives`, `includeLabels`, `includePermissionsForView`,
`acknowledgeAbuse` (only with `alt=media`).

Realistic `fields` value:

```
fields=id,name,mimeType,description,parents,trashed,starred,explicitlyTrashed,size,version,
createdTime,modifiedTime,modifiedByMeTime,viewedByMeTime,webViewLink,webContentLink,
md5Checksum,sha1Checksum,fileExtension,owners(displayName,emailAddress),
lastModifyingUser(displayName,emailAddress),
capabilities(canEdit,canComment,canShare,canDownload,canDelete,canTrash,canCopy),
shared,ownedByMe,driveId,parentIds,exportLinks,properties,appProperties,
shortcutDetails(targetId,targetMimeType)
```

Omitting `fields` returns a **method-specific default** — for `files.list` that is only `kind`,
`id`, `name`, `mimeType`, `resourceKey`. Always set `fields` explicitly in production.

**Scopes (`files.get`, eight):** `drive`, `drive.appdata`, `drive.file`, `drive.meet.readonly`,
`drive.metadata`, `drive.metadata.readonly`, `drive.photos.readonly`, `drive.readonly`.

### (c) List / search — the `q` syntax

`GET https://www.googleapis.com/drive/v3/files?q=<query>&orderBy=...&pageSize=...`

References: <https://developers.google.com/workspace/drive/api/guides/search-files> ·
<https://developers.google.com/workspace/drive/api/guides/ref-search-terms>

Syntax: **`query_term operator values`**.

**Operators (complete):** `contains`, `=`, `!=`, `<`, `<=`, `>`, `>=`, `in`, `and`, `or`, `not`,
`has`.

**File query terms (complete):**

| Term | Valid operators | Notes |
| --- | --- | --- |
| `name` | `contains`, `=`, `!=` | Single-quote the value. |
| `fullText` | `contains` | Name, description, indexable text, and content. |
| `mimeType` | `contains`, `=`, `!=` | |
| `modifiedTime`, `createdTime`, `viewedByMeTime` | `<=`, `<`, `=`, `!=`, `>`, `>=` | RFC 3339; default TZ UTC. `createdTime` is v3-only. |
| `trashed`, `starred`, `sharedWithMe` | `=`, `!=` | Booleans. |
| `parents` | `in` | `'FOLDER_ID' in parents` |
| `owners`, `writers`, `readers` | `in` | Emails/groups. |
| `properties`, `appProperties` | `has` | `has { key='k' and value='v' }` |
| `visibility` | `=`, `!=` | `anyoneCanFind`, `anyoneWithLink`, `domainCanFind`, `domainWithLink`, `limited` |
| `shortcutDetails.targetId` | `=`, `!=` | |

**Escaping:** `'` and `\` inside values must be backslash-escaped:
`name contains 'quinn\'s paper\\essay'`.

**Matching semantics that cause real bugs:**

- `contains` on **`name` is prefix-only**. `name contains 'Hello'` matches `HelloWorld`;
  `name contains 'World'` does **not**.
- `contains` on `fullText` matches **whole tokens** only.
- **Phrase search** requires double quotes *inside* the single quotes:
  `fullText contains '"hello world"'`. Underscores are treated as spaces.

Concrete examples:

1. `'1AbC...' in parents and trashed = false`
2. `mimeType = 'application/vnd.google-apps.document' and modifiedTime > '2023-01-01T00:00:00' and trashed = false` with `orderBy=modifiedTime desc`
3. `name contains 'Project' and (mimeType = 'application/vnd.google-apps.folder' or mimeType = 'application/vnd.google-apps.shortcut')`
4. `'test@example.org' in owners and fullText contains '"quarterly report"' and trashed = false`
5. `appProperties has { key='additionalID' and value='8e8aceg2af2ge72e78' } and trashed = false`

**Key params:** `q`, `pageSize` (default 100; max 1000), `pageToken`, `orderBy`, `spaces`
(`drive`, `appDataFolder`), `corpora` (`user`, `domain`, `drive`, `allDrives`), `driveId`,
`includeItemsFromAllDrives`, `supportsAllDrives`, `fields`, `includeLabels`.

**`orderBy` keys (complete):** `createdTime`, `folder`, `modifiedByMeTime`, `modifiedTime`, `name`,
`name_natural`, `quotaBytesUsed`, `recency`, `sharedWithMeTime`, `starred`, `viewedByMeTime`.
Append `desc` to reverse; comma-separate multiple keys, e.g. `orderBy=folder,modifiedTime desc,name`.
Official warnings: avoid `createdTime` on large collections (use `modifiedTime desc`); omitting
`orderBy` yields **arbitrary** order.

For shared drives set **both** `supportsAllDrives=true` **and** `includeItemsFromAllDrives=true`.
Check `incompleteSearch` in the response — multi-corpora searches may be incomplete.

**Scopes:** same eight as `files.get`.

### (d) Export a Google Workspace file

`GET https://www.googleapis.com/drive/v3/files/{fileId}/export?mimeType=<REQUIRED>`

- `mimeType` is **required**. **Exported content is limited to 10 MB** (confirmed verbatim).
- **Scopes (four):** `drive`, `drive.file`, `drive.meet.readonly`, `drive.readonly`.
  **`drive.metadata.readonly` is NOT accepted** — a metadata-only app cannot export.

| Source | Export MIME types |
| --- | --- |
| Docs | `application/vnd.openxmlformats-officedocument.wordprocessingml.document` (.docx), `application/vnd.oasis.opendocument.text` (.odt), `application/rtf`, `application/pdf`, `text/plain`, `text/html`, `application/zip`, `application/epub+zip`, **`text/markdown`** |
| Sheets | `…spreadsheetml.sheet` (.xlsx), `application/vnd.oasis.opendocument.spreadsheet`, `application/pdf`, `application/zip`, `text/csv` (first sheet only), `text/tab-separated-values` (first sheet only) |
| Slides | `…presentationml.presentation` (.pptx), `application/vnd.oasis.opendocument.presentation`, `application/pdf`, `text/plain` |
| Drawings | `application/pdf`, `image/jpeg`, `image/png`, `image/svg+xml` |

Live discovery: `GET …/about?fields=exportFormats` (and `importFormats`).

**`/export` vs `alt=media` vs `/download`:**

- `/export` → **Google Workspace (Docs editors) files only**, because they have no binary content.
- `GET …/files/{fileId}?alt=media` → **binary/blob files**.
- `POST …/files/{fileId}/download` → LRO-based; **the only way to download Google Vids files**
  (exporting Vids returns `fileNotExportable`).

### (e) Share — create a permission

`POST https://www.googleapis.com/drive/v3/files/{fileId}/permissions`

Body fields: `role`, `type`, `emailAddress` (required for `user`/`group`), `domain` (required for
`domain`), `allowFileDiscovery`, `expirationTime`, `inheritedPermissionsDisabled`, `pendingOwner`,
`view`.

- `type`: `user`, `group`, `domain`, `anyone`
- `role`: `owner`, `organizer`, `fileOrganizer`, `writer`, `commenter`, `reader`

⚠️ **Correction worth noting: `sendNotificationEmail`, `emailMessage`, `transferOwnership` and
`moveToNewOwnersRoot` are QUERY parameters, not body fields** (verified in the Discovery document),
even though they are frequently listed as body fields in third-party write-ups.

- `sendNotificationEmail` — defaults to `true` for users/groups; **not allowed** for other types;
  must not be disabled for ownership transfer.
- `transferOwnership` — "required as an acknowledgement of the side effect".

Update is a **separate path**: `PATCH https://www.googleapis.com/drive/v3/files/{fileId}/permissions/{permissionId}`
(params: `supportsAllDrives`, `useDomainAdminAccess`, `transferOwnership`, `removeExpiration`).

**Scopes (create and update): exactly two — `drive`, `drive.file`.** `drive.readonly` and
`drive.metadata*` cannot modify permissions.

Operational note: concurrent permission writes on the same item are unsupported — expect
`sharingRateLimitExceeded`.

### (f) Delete / trash

| Operation | Call |
| --- | --- |
| Permanent delete | `DELETE https://www.googleapis.com/drive/v3/files/{fileId}` |
| Trash | `PATCH https://www.googleapis.com/drive/v3/files/{fileId}` with `{"trashed": true}` (untrash: `false`) |
| Empty trash | `DELETE https://www.googleapis.com/drive/v3/files/trash` |

- **Only the file owner can trash a file**; otherwise `insufficientFilePermissions`. Check
  `fields=ownedByMe` first (not populated for shared-drive items).
- Trashed items are auto-deleted after **30 days**.
- Shared drives need `supportsAllDrives=true`. Shared-drive permanent delete needs `role=organizer`
  on the parent folder.
- ⚠️ **`files.emptyTrash` accepts exactly ONE scope: `https://www.googleapis.com/auth/drive`.**
  Not `drive.file`, not `drive.readonly`. If you want to stay on `drive.file`, do not expose
  "empty trash".

**Scopes:** `files.delete` → `drive`, `drive.appdata`, `drive.file`.
`files.update` (trash) → `drive`, `drive.appdata`, `drive.file`, `drive.metadata`, `drive.scripts`.

### (g) Copy a file

`POST https://www.googleapis.com/drive/v3/files/{fileId}/copy`

Body is a `File`: `name`, `parents[]`, `mimeType`, `description`, `id`.

Query params: `supportsAllDrives`, `ignoreDefaultVisibility`, `keepRevisionForever`,
`ocrLanguage`, `copyComments`, `includeLabels`, `includePermissionsForView`, `fields`;
deprecated `enforceSingleParent`, `supportsTeamDrives`.

**Scopes (four):** `drive`, `drive.appdata`, `drive.file`, `drive.photos.readonly`.

Limits: **folders cannot be copied**; one parent maximum; `copyComments` copies only *open* comments
and only for Docs/Sheets/Slides; the copy gets independent ACLs owned by the creator; third-party
shortcuts can never be copied.

### (h) Scope matrix

| Action | Method | `drive` | `drive.file` | `drive.readonly` | `drive.metadata.readonly` |
| --- | --- | :---: | :---: | :---: | :---: |
| Upload/create + folder | `files.create` | ✅ | ✅ | — | — |
| Get metadata | `files.get` | ✅ | ✅ | ✅ | ✅ |
| List/search | `files.list` | ✅ | ✅ | ✅ | ✅ |
| Export | `files.export` | ✅ | ✅ | ✅ | **—** |
| Create/update permission | `permissions.create`/`update` | ✅ | ✅ | **—** | **—** |
| Delete | `files.delete` | ✅ | ✅ | — | — |
| Trash | `files.update` | ✅ | ✅ | — | — |
| **Empty trash** | `files.emptyTrash` | ✅ | **—** | **—** | **—** |
| Copy | `files.copy` | ✅ | ✅ | — | — |
| `changes.list` / `changes.watch` | — | ✅ | ✅ | ✅ | ✅ |

### (i) Scope strategy — `drive.file` vs `drive.readonly` vs `drive`

| Scope | Sensitivity | What it grants |
| --- | --- | --- |
| `…/auth/drive.file` | **Non-sensitive** | "See, edit, create, and delete only the specific Google Drive files you use with this app." Drive-specific expansion: "Create new Drive files, or modify existing files, that you open with an app or that the user shares with an app while using the Google Picker API or the app's file picker." |
| `…/auth/drive.readonly` | **Restricted** | View and download **all** your Drive files. |
| `…/auth/drive` | **Restricted** | View and manage **all** your Drive files. |
| `…/auth/drive.metadata.readonly` | **Restricted** | View metadata for files in your Drive. |

Verification burden: non-sensitive needs only basic OAuth app verification. **Restricted scopes need
restricted-scope verification, and: "If you store restricted scope data on servers (or transmit),
then you must go through a security assessment."** For a server-side Go service this is the decisive
constraint.

**`drive.file` limitations:** it is a **per-file grant, not per-Drive**. `files.list`/`files.get`
accept the scope mechanically, but the visible file set is limited to files your app created or the
user opened/shared with it. It cannot enumerate an existing Drive. It **is** accepted by
`changes.watch`, `changes.list` and `changes.getStartPageToken` — but no official guide documents a
`drive.file`-specific change-log restriction, and whether the resulting feed is *useful* under
per-file access is not stated. **Verify this empirically before relying on it.**

**Incremental authorization:** not documented in the Drive guides. The mechanism is
`include_granted_scopes=true` on the authorization request (Identity docs), which lets you request
`drive.file` up front and escalate later in context.

## 1.7 The properly-authenticated alternative: Google Workspace Events API

Guide: <https://developers.google.com/workspace/drive/api/guides/events-overview> ·
<https://developers.google.com/workspace/events>

If your service genuinely needs authenticated, verifiable event delivery, Drive offers a **second,
newer subscription mechanism** that routes through Cloud Pub/Sub instead of an unauthenticated
webhook:

`POST https://workspaceevents.googleapis.com/v1/subscriptions`

```jsonc
{
  "targetResource": "//drive.googleapis.com/files/FILE_ID",   // or //drive.googleapis.com/drives/DRIVE_ID
  "eventTypes": ["google.workspace.drive.file.v3.contentChanged"],
  "notificationEndpoint": { "pubsubTopic": "projects/PROJECT_ID/topics/TOPIC_ID" },
  "payloadOptions": { "includeResource": false },
  "driveOptions": { "includeDescendants": true }
}
```

- Requires the **Google Workspace Events API**, **Pub/Sub API** and **Drive API** enabled, plus a
  Pub/Sub topic with `roles/pubsub.publisher` granted to
  **`drive-api-event-push@system.gserviceaccount.com`**.
- Drive supported event families: **Access proposals, Approvals, Comments, Files, Replies**.
- Events arrive as **CloudEvents** wrapped in Pub/Sub messages, and Pub/Sub push subscriptions
  support **OIDC JWT authentication** of the delivery — this is the verifiable-delivery path.
- **Subscription TTL (documented):** *"Include resource data → Up to 4 hours, or 24 hours if you use
  domain-wide delegation. Exclude resource data → Up to 7 days."* Renew via `subscriptions.update()`
  setting the `ttl` field to the maximum.
- `includeDescendants` is only valid when `targetResource` is a file or shared drive whose MIME type
  is `application/vnd.google-apps.folder`; it cannot be set on the My Drive root or on shared-drive
  roots.
- Limitations: events fire even for deeply nested descendants, but **only for resources the
  authorizing user can access**; the root folder of a shared drive is not subscribable.

**Recommendation:** if the requirement is "a service that must *verify* a delivery", prefer the
Workspace Events API + Pub/Sub push with OIDC. Use Drive `watch` only if you accept the
token-as-shared-secret model and re-fetch state through the authenticated API.

---

# Part 2 — Notion

## 2.1 Webhooks — yes, they exist; no, you cannot create them via API

Reference: <https://developers.notion.com/reference/webhooks> ·
<https://developers.notion.com/reference/webhooks-events-delivery>

### Creation is dashboard-only — stated explicitly

**There is no public REST API to create, list, update, verify or delete webhook subscriptions.**
Evidence:

- Notion's full OpenAPI spec (`https://developers.notion.com/openapi.json`, 48 paths) and the
  undocumented spec (50 paths) contain **zero** paths matching `webhook`, and there is no
  `Webhooks` tag in the tag list.
- The documentation index <https://developers.notion.com/llms.txt> lists no subscription endpoint.
- The only documented creation flow is the UI: connection settings →
  **<https://app.notion.com/developers/connections>** → **Webhooks** tab →
  **+ Create a subscription** → enter a public HTTPS URL (localhost unreachable) → choose event
  types → **Create subscription**.

The `/reference/webhooks/*` pages are **inbound event payload schemas**, not endpoints.

### Verification handshake

On creation Notion sends a one-time `POST` containing:

```json
{ "verification_token": "secret_tMrlL1qK5vuQAh1b6cZGhFChZTSYJlce98V0pYn7yBl" }
```

You must surface that token, paste it into the modal, and click **Verify subscription**. If you
miss it, use **Resend token**. The subscription only becomes active after this.

**After verification the webhook URL cannot be changed** — you must delete and recreate the
subscription. Subscribed event types *can* be changed at any time.

### Delivery verification — signature spec

Every event carries an `X-Notion-Signature` header:

```
X-Notion-Signature: sha256=461e8cbcba8a75c3edd866f0e71280f5a85cbf21eff040ebd10fe266df38a735
```

- **Algorithm:** HMAC-SHA256.
- **Key:** the `verification_token` from the handshake.
- **Signed string:** the **raw request body** — exactly the bytes received. The docs are explicit:
  *"Pass the raw request body exactly as it arrived (re-serialized JSON produces different bytes and
  fails verification)."*
- **Header format:** `sha256=` prefix followed by lowercase hex digest.
- **Comparison:** constant-time (`hmac.Equal` in Go, `timingSafeEqual` in the docs' JS sample).

Go implementation sketch:

```go
mac := hmac.New(sha256.New, []byte(verificationToken))
mac.Write(rawBody) // raw bytes, before any JSON unmarshalling
expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
if !hmac.Equal([]byte(expected), []byte(r.Header.Get("X-Notion-Signature"))) {
    // reject
}
```

⚠️ **Replay window: NOT documented.** Notion's docs show **no timestamp header and no
replay/clock-skew guidance** — unlike Stripe's `Stripe-Signature` (`t=…,v1=…`), which is
timestamped. Notion's official samples verify the signature only. Practical consequence: a valid
signature can be replayed indefinitely. Defend by deduplicating on the event `id` (a UUID) and
tracking `timestamp` yourself, since a signed body is at least tamper-proof.

Notion is not the only one aware of the ergonomics gap: the JS SDK exposes
`verifyWebhookSignature({ body, signature, verificationToken })` and `signWebhookPayload()` (for
tests) from `@notionhq/client` v5.23.0+. **There is no official Go sample** — the docs ship
JavaScript, Python and Ruby only.

### Envelope fields (all event types)

| Field | Type | Description |
| --- | --- | --- |
| `id` | UUID | Unique event ID. **Use for deduplication.** |
| `timestamp` | String | ISO 8601 time the event occurred. Use to reorder. |
| `workspace_id` | UUID | Originating workspace. |
| `workspace_name` | String | (Present in all sample payloads.) |
| `subscription_id` | UUID | The webhook subscription. |
| `integration_id` | UUID | The connection the subscription belongs to. |
| `type` | String | e.g. `page.created`. |
| `authors` | Array | `{id, type}` where type is `person`, `bot`, or `agent`. Usually length 1; more for aggregated events. |
| `accessible_by` | Array | `{id, type}` of bots/users who own the connection and can access `entity`. **Only for public connections.** |
| `attempt_number` | Number | Delivery attempt, **1–8**. |
| `api_version` | String | Present on newer events, e.g. `2025-09-03`. |
| `entity` | Object | `{id, type}` of the triggering object; type is `page`, `block`, `database`, or `data_source`. |
| `data` | Object | Event-specific payload. |

**Events do not carry content.** They are a signal; you must call the API to fetch current state.

### Event types

| Type | Aggregated? |
| --- | --- |
| `page.content_updated` | Yes |
| `page.created` | Yes |
| `page.deleted` | Yes |
| `page.locked` | No |
| `page.moved` | Yes |
| `page.properties_updated` | Yes |
| `page.undeleted` | Yes |
| `page.unlocked` | No |
| `database.content_updated` | Yes — **deprecated in `2025-09-03`** |
| `database.created` | Yes |
| `database.deleted` | Yes |
| `database.moved` | Yes |
| `database.schema_updated` | Yes — **deprecated in `2025-09-03`** |
| `database.undeleted` | Yes |
| `data_source.content_updated` | Yes — **new in `2025-09-03`** |
| `data_source.created` | Yes — new in `2025-09-03` |
| `data_source.deleted` | Yes — new in `2025-09-03` |
| `data_source.moved` | Yes — new in `2025-09-03` |
| `data_source.schema_updated` | Yes — new in `2025-09-03` |
| `data_source.undeleted` | Yes — new in `2025-09-03` |
| `comment.created` | No — requires the **Read comments** capability |
| `comment.deleted` | No |
| `comment.updated` | No |
| `view.created` / `view.updated` / `view.deleted` | Available on `2025-09-03`+ (per changelog) |
| `file_upload.created` / `.completed` / `.expired` / `.failed` | Payload pages exist in the docs index |
| `page.transcription_block_transcript_deleted` | Payload page exists in the docs index |

⚠️ The event table in `webhooks-events-delivery.md` is **incomplete** — it omits the view and
file-upload events that exist as reference pages in `llms.txt`. New event types are **not**
auto-subscribed: you must update the subscription in the Webhooks tab.

### Delivery behaviour

- Aggregated events are batched within a short window; expect **≤ ~1 minute** delay typically.
- "Events should be delivered **within 5 minutes** of their occurrences."
- **Ordering is not guaranteed** — reorder by `timestamp`.
- **At-most-once intent**, retried up to **8 times** with exponential backoff; final retry
  **~24 hours** after the trigger.
- Events require the connection to have **access to the object**; events on private pages the
  connection cannot see are never delivered.

## 2.2 `Notion-Version` — the current value is `2026-03-11`

Source: <https://developers.notion.com/reference/versioning>

**`Notion-Version: 2026-03-11`** is the latest. The docs state verbatim: *"our latest version is
2026-03-11"* and *"The most recent `Notion-Version` is 2026-03-11."*

- The header is **required on every REST request**. Omitting it returns **HTTP 400 with code
  `missing_version`**.
- `2025-09-03` is real, still accepted, and has no announced deprecation — but it is **not** the
  latest. The OpenAPI enum currently advertises only `2026-03-11`.
- A second optional header `Notion-Beta` exists for opt-in betas (e.g.
  `Notion-Beta: notion-as-code-2026-07-31`). Not needed for the actions below.

### `2026-03-11` breaking changes vs `2025-09-03` (exactly three)

| Change | `2025-09-03` | `2026-03-11` |
| --- | --- | --- |
| Block placement | `after` string | **`position` object** (`{"type":"end"}` / `{"type":"start"}` / `{"type":"after_block","after_block":{"id":"…"}}`) |
| Trash field | `archived` accepted as deprecated alias | **`archived` removed — use `in_trash`** |
| Block type name | `transcription` | **`meeting_notes`** |

Webhook payloads are identical across the two versions.

## 2.3 Actions — exact REST calls and bodies

Base: `https://api.notion.com` · All require `Authorization: Bearer <token>` and
`Notion-Version: 2026-03-11`.

### Database vs data source — read this first

Since `2025-09-03`, Notion split the concepts:

- **Database** = the *container* (`object: "database"`), holding `data_sources[{id, name}]`.
- **Data source** = the *table/schema* (`object: "data_source"`); its `parent` is a `database_id`.
- **Pages are rows under a `data_source_id`**, not directly under a database.
- Permissions are granted on the **database**, not the individual data source.

Discover the data source IDs with `GET /v1/databases/{database_id}` → `data_sources[].id`.

**Recommendation: always send `data_source_id` as the parent.** The `database_id` parent variant is
still accepted by the `2026-03-11` spec, but the docs never state how it resolves for a
multi-source database.

### (a) Create a page in a database / data source

`POST /v1/pages`

```json
{
  "parent": { "type": "data_source_id", "data_source_id": "d9824bdc-8445-4327-be8b-5b47500af6ce" },
  "properties": {
    "Name":  { "title": [{ "text": { "content": "Q3 Planning" } }] },
    "Status": { "select": { "name": "In progress" } },
    "Due":   { "date": { "start": "2026-04-01" } }
  },
  "children": [
    { "object": "block", "type": "paragraph",
      "paragraph": { "rich_text": [{ "text": { "content": "Kickoff notes." } }] } }
  ]
}
```

Property keys must match the parent data source's schema exactly. `children` max 100.
`filter_properties` is available as a query param.

### (b) Create a page under a parent page

Same endpoint, different parent. **`title` is the only valid property** when the parent is a page:

```json
{
  "parent": { "type": "page_id", "page_id": "0ef104cd-477e-80e1-8571-cfd10e92339a" },
  "properties": { "title": [{ "text": { "content": "Meeting notes" } }] }
}
```

Internal connections **must** supply a parent. Public connections and PATs may omit it (or send
`parent[workspace]=true`) to create a private workspace-level page.

### (c) Retrieve a page

`GET /v1/pages/{page_id}` — returns **properties, not content**. Query param `filter_properties`.

To read content, call the **blocks** API passing the **page ID as `block_id`**:
`GET /v1/blocks/{block_id}/children`. First level only — recurse where `has_children: true`.

For a single property with many references: `GET /v1/pages/{page_id}/properties/{property_id}`
(max 25 references per property in the page object).

Returns **404** if the page is missing **or** simply not shared with the connection.

### (d) Update page properties

`PATCH /v1/pages/{page_id}`

```json
{
  "properties": {
    "Status": { "select": { "name": "Done" } }
  }
}
```

No required fields. Also accepts `icon`, `cover`, `is_locked`, `template`, `erase_content`,
`in_trash`. Query param `filter_properties`. Rollup/created_time/last_edited_time properties are
**not** writable. The parent cannot be changed here — use `POST /v1/pages/{page_id}/move`.

### (e) Query a database / data source

`POST /v1/data_sources/{data_source_id}/query`

> The legacy `POST /v1/databases/{database_id}/query` is **deprecated as of `2025-09-03`** and is
> **absent from the current OpenAPI spec**. Use the data-source path.

```json
{
  "filter": {
    "and": [
      { "property": "Status", "select": { "equals": "In progress" } },
      { "property": "Due", "date": { "on_or_before": "2026-04-01" } },
      {
        "or": [
          { "property": "Priority", "number": { "greater_than": 2 } },
          { "property": "Tags", "multi_select": { "contains": "urgent" } }
        ]
      }
    ]
  },
  "sorts": [
    { "property": "Due", "direction": "ascending" },
    { "timestamp": "last_edited_time", "direction": "descending" }
  ],
  "page_size": 100,
  "start_cursor": "…"
}
```

Body: `sorts[]`, `filter`, `start_cursor`, `page_size`, `is_archived`, `result_type`
(`page` | `data_source`). Query param `filter_properties`.

**Compound filters**: `and` / `or` arrays, **nesting limited to 2 levels**.

**Operators by property type:**

| Type | Operators |
| --- | --- |
| `checkbox` | `equals`, `does_not_equal` |
| `date` | `after`, `before`, `equals`, `on_or_after`, `on_or_before`, `is_empty`, `is_not_empty`, `next_week`, `next_month`, `next_year`, `past_week`, `past_month`, `past_year`, `this_week` — plus relative values `"today"`, `"tomorrow"`, `"yesterday"`, `"one_week_ago"`, `"one_week_from_now"`, `"one_month_ago"`, `"one_month_from_now"` |
| `number` | `equals`, `does_not_equal`, `greater_than`, `greater_than_or_equal_to`, `less_than`, `less_than_or_equal_to`, `is_empty`, `is_not_empty` |
| `rich_text`, `title` | `contains`, `does_not_contain`, `equals`, `does_not_equal`, `starts_with`, `ends_with`, `is_empty`, `is_not_empty` |
| `select`, `multi_select`, `status` | `equals`, `does_not_equal`, `contains`, `does_not_contain`, `is_empty`, `is_not_empty` |
| `people`, `created_by`, `last_edited_by` | `contains`, `does_not_contain`, `is_empty`, `is_not_empty` (UUID or `"me"`) |
| `relation` | `contains`, `does_not_contain`, `is_empty`, `is_not_empty` |
| `files` | `is_empty`, `is_not_empty` |
| `formula` | nested `checkbox`/`date`/`number`/`string` conditions |
| `rollup` | `any`, `every`, `none`, or nested `date`/`number` |
| `verification` | `status`, `does_not_equal` (`verified` \| `expired` \| `none`) |
| `unique_id` | `equals`, `does_not_equal`, `greater_than`, … |
| `timestamp` | `created_time` \| `last_edited_time` wrapping a date condition — **must not include `property`** |

**Sorts**: `{property, direction}` or `{timestamp: created_time|last_edited_time, direction}`.

**Hard limits:**

- **10,000 results per query.** Beyond that `has_more` becomes `false` and
  `request_status.type` is `"incomplete"` with `"incomplete_reason": "query_result_limit_reached"`
  (or `query_result_limit_reached`-style reason). **Check `request_status` on every page** — any
  incomplete page means the whole query was capped, not just that page.
- Workaround for large sets: partition the query into `created_time` windows.
- Archived rows are returned by setting top-level `is_archived: true`. **`in_trash` is NOT a valid
  query body param** — `is_archived` (archive) and `in_trash` (trash) are different concepts.

### (f) Retrieve a database

`GET /v1/databases/{database_id}` → database object with `data_sources[{id, name}]`,
`database_type`, `is_inline`, `in_trash`, `is_locked`.

For the schema itself: `GET /v1/data_sources/{data_source_id}` → `properties`, `parent`,
`database_parent`.

### (g) Append block children

`PATCH /v1/blocks/{block_id}/children`

```json
{
  "children": [
    { "object": "block", "type": "heading_2",
      "heading_2": { "rich_text": [{ "text": { "content": "Summary" } }] } },
    { "object": "block", "type": "paragraph",
      "paragraph": { "rich_text": [{ "text": { "content": "Body text." } }] } }
  ],
  "position": { "type": "end" }
}
```

- `children` is **required**, **max 100 per request**.
- `position`: `{"type":"end"}` (default) | `{"type":"start"}` |
  `{"type":"after_block","after_block":{"id":"…"}}`.
- Limits: **max two levels of nesting** per request; payload ceiling **1000 block elements / 500 KB**.
- `after` is deprecated on `2026-03-11`; sending both `after` and `position` is an error.

### (h) Retrieve block children

`GET /v1/blocks/{block_id}/children?page_size=100&start_cursor=…`

`page_size` default 100, max 100. Returns `type: "block"`, `results[]`, `next_cursor`, `has_more`.
Recurse into children where `has_children: true`.

### (i) Search

`POST /v1/search`

```json
{
  "query": "roadmap",
  "filter": { "property": "object", "value": "page" },
  "sort": { "timestamp": "last_edited_time", "direction": "descending" },
  "page_size": 50,
  "start_cursor": "…"
}
```

- `filter` is either `{"property":"object","value":"page"|"data_source"}` (optionally with
  `"in_trash": true`) **or** just `{"in_trash": true}` to list trashed content.
- `sort` is `{timestamp, direction}` or `{property: "relevance"}`.
- Searches only content **shared with the connection**.

### (j) Archive / delete a page

**There is no permanent delete.** The docs state the API does not support permanently deleting pages.

`PATCH /v1/pages/{page_id}` with:

```json
{ "in_trash": true }
```

Restore with `{"in_trash": false}`. On `2026-03-11`, `archived` is **rejected** (fully removed). On
`2025-09-03` it still worked as a deprecated alias.

Blocks can be trashed with `DELETE /v1/blocks/{block_id}`, which requires **Update content**.

## 2.4 Capabilities / permissions

Source: <https://developers.notion.com/reference/capabilities>

Exact documented capability names:

| Group | Capabilities |
| --- | --- |
| Content | **Read content**, **Update content**, **Insert content** |
| Comments | **Read comments**, **Insert comments** (off by default) |
| User | **No user information**, **User information without email addresses**, **User information with email addresses** |

### Action → capability map

| Action | Endpoint | Capability |
| --- | --- | --- |
| Create page | `POST /v1/pages` | **Insert content** |
| Retrieve page | `GET /v1/pages/{page_id}` | **Read content** |
| Update page properties / trash | `PATCH /v1/pages/{page_id}` | **Update content** |
| Query data source | `POST /v1/data_sources/{id}/query` | **Read content** |
| Retrieve database | `GET /v1/databases/{id}` | **Read content** |
| Retrieve data source | `GET /v1/data_sources/{id}` | **Read content** |
| Append block children | `PATCH /v1/blocks/{id}/children` | **Insert content** |
| Retrieve block children | `GET /v1/blocks/{id}/children` | **Read content** |
| Delete block | `DELETE /v1/blocks/{id}` | **Update content** |
| Search | `POST /v1/search` | Not stated per-endpoint; **inferred Read content** |
| Create / list comments | `/v1/comments` | **Insert comments** / **Read comments** |

### Failure modes — and how to tell them apart in Go

- **Missing capability → HTTP 403**, code `restricted_resource`
  (*"API token does not have access to this resource."*).
- **Not shared with the connection → HTTP 404**, code `object_not_found` (naming the connection).

So: **403 = capability problem, 404 = sharing problem.** That distinction is worth encoding in your
error handling.

Sharing: internal connections have **zero access by default**. Grant access via the Developer portal
**Content access** tab, or in Notion via page ••• → **Connections** → **+ Add connection**. Child
pages inherit access. **To query a data source, its parent database must be shared.** PATs act as
their creator and need no bot-style sharing.

Design guidance from the docs: request minimum capabilities. Insert-only connections should not hold
Read content; export-only connections should not hold Update content. Fewer capabilities means
workspace admins are more likely to install the connection.

## 2.5 Authentication

### REST calls

```
Authorization: Bearer <token>
Notion-Version: 2026-03-11
```

The **same** `Bearer` scheme is used for internal connection tokens, personal access tokens, and
OAuth2 access tokens.

Token prefixes: new tokens use **`ntn_`** (changed 2024-09-25); legacy `secret_` tokens still work;
refresh tokens appear as **`nrt_`**. The docs explicitly say to treat tokens as **opaque strings**
and **not** to regex-validate them.

### OAuth2 flow

| Step | Call |
| --- | --- |
| Authorize | `https://api.notion.com/v1/oauth/authorize?owner=user&client_id=…&redirect_uri=…&response_type=code&state=…` |
| Exchange code | `POST https://api.notion.com/v1/oauth/token` |
| Refresh | `POST https://api.notion.com/v1/oauth/token` |

Both token calls use **HTTP Basic auth** — `Authorization: Basic base64(CLIENT_ID:CLIENT_SECRET)` —
**not** Bearer.

```jsonc
// Exchange
{ "grant_type": "authorization_code", "code": "…", "redirect_uri": "https://example.com/auth/notion/callback" }

// Refresh
{ "grant_type": "refresh_token", "refresh_token": "nrt_…" }
```

`redirect_uri` is required if it was supplied as a query param in the authorize URL or if the
connection has multiple redirect URIs; it is **not allowed** if the connection has exactly one and
no query param was sent.

⚠️ **Notion OAuth2 DOES use refresh tokens** — a common assumption is that it does not. The response
includes a required `refresh_token`, and a refresh returns a **new access token AND a new refresh
token**. Persist the new pair atomically.

Also in the spec: `POST /v1/oauth/introspect`, `POST /v1/oauth/revoke`.

### Rate limits

Per-connection, fixed 60-second window: **600 req/min** on Business and Enterprise plans,
**180 req/min** on all other plans. A shared per-workspace limit also applies. `429` responses carry
`additional_data.rate_limit_reason` and `additional_data.retry_after`; `Retry-After` never exceeds
60 s. **`529` = `service_overload`** — retry like a `429`. The docs include a Go retry sample.

---

# 3. Unconfirmed — explicitly flagged

Nothing below is asserted as fact anywhere in this report; each is a genuine gap.

## Google Drive

1. **Maximum / default `watch` channel TTL is not published.** No official statement on the push
   guide, the `changes.watch`/`files.watch` reference pages, or the Drive limits page. The ~24 h
   figure is community-reported only.
2. **No official endpoint-level statement** that `files.list` under `drive.file` returns only
   app-created/opened files. The per-file restriction is documented only at the *scope* level. The
   engineering conclusion is sound but is an inference.
3. **`drive.file` + change-log semantics are undocumented.** The scope is mechanically accepted by
   `changes.watch`/`list`/`getStartPageToken`, but no guide says whether the resulting feed is
   semantically useful under per-file access. **Test empirically.**
4. **Incremental authorization is absent from the Drive guides**; the mechanism comes from the
   Identity docs. No Drive-specific worked example or Go sample was verified.
5. **Which conversions `files.copy` permits via body `mimeType`** is not documented.
6. **`ocrLanguage` format conflicts:** the guide says BCP 47, Discovery says ISO 639-1.
7. **Service-account file ownership conflicts:** the create-file guide says the service account owns
   the created file; the folder guide says "service accounts can't own any files."
8. The documented **default `fields` set for `files.get`** is not stated.
9. **Apparent Discovery defect:** `Permission.emailAddress` / `domain` are marked `readOnly: true`,
   contradicting their own descriptions and the sharing guide.
10. **Doc defect:** the download guide references `drive.readonly.metadata`, which is **not a real
    scope** (the correct one is `drive.metadata.readonly`).

## Notion

11. **How a `database_id` parent resolves for a multi-source database** on `2026-03-11`. The spec
    still allows it; the docs never explain the resolution. → Always send `data_source_id`.
12. **What writing `is_archived` actually does.** `reference/page.md` and the OpenAPI contradict each
    other on `is_archived` vs `in_trash`. → Use `in_trash`.
13. **Search's response `type` string:** the OpenAPI says `page_or_data_source`; `intro.md`'s
    pagination table says `page_or_database`.
14. **Search's required capability** is not stated per-endpoint (inferred Read content).
15. Capabilities for retrieve-data-source, retrieve-block and update-block are inferred.
16. **Whether pre-`2026-03-11` versions are accepted at runtime** — prose says yes, the OpenAPI enum
    advertises only `2026-03-11`.
17. **Full capability list is uncertain:** the changelog mentions a *"View sessions and interact with
    agents"* capability absent from `capabilities.md`; `webhooks.md` says *"comment read"* where
    `capabilities.md` says *"Read comments"*.
18. `page_size` bounds are stated only in prose (100 / 100); the OpenAPI types them as unconstrained
    numbers.
19. Whether `POST /v1/pages` `content` is an alias or a legacy name for `children` (both are
    `array<blockObjectRequest>`, max 100).
20. **No official Go webhook-signature sample exists** — JS, Python and Ruby only.
21. **Nothing in this report was executed against a live Google or Notion account.** All findings are
    documentation- and spec-derived.

---

# 4. Recommended Go client configuration

```go
// Notion
const notionVersion = "2026-03-11"        // NOT 2025-09-03
// Authorization: Bearer ntn_…
// Webhooks: RECEIVE only. Verify X-Notion-Signature = "sha256=" + hex(HMAC-SHA256(rawBody, verificationToken))
//           constant-time compare; dedupe on event "id" (no replay window is documented).
// Pages:    parent {"type":"data_source_id"} — not database_id
// Query:    POST /v1/data_sources/{id}/query   (the /v1/databases/{id}/query path is deprecated)
// Append:   "position" object, not "after"
// Delete:   {"in_trash": true}, never "archived"
// Refresh:  grant_type=refresh_token → store the NEW access AND refresh token

// Google Drive
// Watch:    changes.watch requires pageToken from changes.getStartPageToken
// Security: no HMAC exists. Treat notifications as an unauthenticated hint.
//           Verify X-Goog-Channel-Token (constant-time), then re-fetch via changes.list.
// Renewal:  no auto-renewal and no published max TTL — re-watch every 6–12 h with a NEW channel id,
//           and persist resourceId + expiration from the watch response.
// Scopes:   drive.file (non-sensitive) for create/read/search/export/copy/delete/permissions.
//           Avoid files.emptyTrash — it requires the full restricted "drive" scope.
//           Restricted scopes require a security assessment if you store data server-side.
// Better:   If you need VERIFIABLE delivery, use the Workspace Events API + Pub/Sub with OIDC
//           (subscriptions.create on workspaceevents.googleapis.com/v1/subscriptions).
```
