# Google Drive API v3 — Verified Reference for a Go Service

**Research date:** 2026
**Primary authority:** [Drive API v3 Discovery document](https://www.googleapis.com/discovery/v1/apis/drive/v3/rest) — fetched 2026, `name: drive`, `version: v3`, **`revision: 20260904`**, `rootUrl: https://www.googleapis.com/`, `servicePath: drive/v3/`.
**Secondary authority:** official guides on `developers.google.com` (each cited inline).

> **Verification method.** Every endpoint path, query-parameter name, request-body field name and scope string below was extracted directly from the Discovery JSON (parsed programmatically) and/or quoted from the official guide. Anything I could not confirm is listed in [§9](#9-could-not-confirm). Scope strings are reproduced exactly as they appear in the Discovery document's `auth.oauth2.scopes`.

---

## 0. Baseline facts

| Item | Value | Source |
| --- | --- | --- |
| API root for metadata calls | `https://www.googleapis.com/drive/v3/` | Discovery `rootUrl` + `servicePath` |
| Upload root | `https://www.googleapis.com/upload/drive/v3/files` | Discovery `files.create.mediaUpload.protocols` |
| Resumable upload path | `/resumable/upload/drive/v3/files` (session URI is returned in the `Location` header) | Discovery `mediaUpload.protocols.resumable.path` |
| Max media upload size | `5497558138880` bytes = **5120 GiB (5 TiB)** | Discovery `files.create.mediaUpload.maxSize` |
| `uploadType` accepted values | `media`, `multipart`, `resumable` | Guides; Discovery global param `uploadType` |
| Default page size (`files.list`) | `100`; max `1000` (values above 1000 are coerced to 1000) | Discovery `files.list.pageSize` |

OAuth scopes recognized by the API (exact strings, from Discovery `auth.oauth2.scopes`):

```
https://www.googleapis.com/auth/drive
https://www.googleapis.com/auth/drive.file
https://www.googleapis.com/auth/drive.readonly
https://www.googleapis.com/auth/drive.metadata
https://www.googleapis.com/auth/drive.metadata.readonly
https://www.googleapis.com/auth/drive.appdata
https://www.googleapis.com/auth/drive.photos.readonly
https://www.googleapis.com/auth/drive.meet.readonly
https://www.googleapis.com/auth/drive.scripts
https://www.googleapis.com/auth/drive.apps.readonly
```

---

## 1. Upload / create a file

Guide: [Upload file data](https://developers.google.com/workspace/drive/api/guides/manage-uploads) · [Create and manage files](https://developers.google.com/workspace/drive/api/guides/create-file) · Reference: [`files.create`](https://developers.google.com/workspace/drive/api/reference/rest/v3/files/create)

### 1.1 Simple upload — `uploadType=media`

```
POST https://www.googleapis.com/upload/drive/v3/files?uploadType=media
```

- **Purpose:** "transfer a small media file (**5 MB or less**) without supplying metadata."
- **Body:** the raw file bytes.
- **Headers (both named explicitly in the guide):**
  - `Content-Type` — "Set to the MIME media type of the object being uploaded."
  - `Content-Length` — "Set to the number of bytes you upload. If you use chunked transfer encoding, this header is not required."
- **Response:** `HTTP 200 OK` plus the file's metadata.
- Basic metadata is created and attributes such as the MIME type or `modifiedTime` are inferred.

### 1.2 Multipart upload — `uploadType=multipart`

```
POST https://www.googleapis.com/upload/drive/v3/files?uploadType=multipart
```

- **Purpose:** "upload metadata and data in the same request … if the data you send is small enough to upload again, in its entirety, if the connection fails." Also **5 MB or less**.
- **Top-level header:** `Content-Type: multipart/related; boundary=foo_bar_baz` (guide uses `foo_bar_baz` as the example boundary) and `Content-Length` set to the total request-body size.
- **Body is `multipart/related` ([RFC 2387](http://tools.ietf.org/html/rfc2387)) with exactly two parts in this order:**
  1. **Metadata** — "must come first and must have a `Content-Type` header set to `application/json; charset=UTF-8`. Add the file's metadata in JSON format."
  2. **Media** — "must come second and must have a `Content-Type` header of any MIME type."
  - Separate parts with a boundary string "preceded by two hyphens", and "add two hyphens after the final boundary string."

### 1.3 Resumable upload — `uploadType=resumable`

**Step 1 — initiate the session:**

```
POST https://www.googleapis.com/upload/drive/v3/files?uploadType=resumable
```

- **Purpose:** "for large files (**greater than 5 MB**) and when there's a high chance of network interruption". The guide adds they "also work for small files at a minimal cost of one additional HTTP request per upload."
- **Body:** the file metadata in JSON, or empty if you have none.
- **Headers (exact names from the guide):**
  - `X-Upload-Content-Type` — *Optional.* MIME type of the file data transferred in subsequent requests. "If the MIME type of the data is not specified in the metadata or through this header, the object is served as `application/octet-stream.`"
  - `X-Upload-Content-Length` — *Optional.* Number of bytes of file data transferred in subsequent requests.
  - `Content-Type` — "Required if you have metadata for the file. Set to `application/json; charset=UTF-8`."
  - `Content-Length` — "Required unless you use chunked transfer encoding."
- **Response:** `HTTP/1.1 200 OK`, empty body, and a `Location` header holding the resumable session URI, e.g.
  `Location: https://www.googleapis.com/upload/drive/v3/files?uploadType=resumable&upload_id=xa298sd_sdlkj2`
- **"A resumable session URI expires after one week."**
- To update an existing file, use `PATCH` for the initial request.

**Step 2 — upload the content:** `PUT` to the session URI.

- *Single request:* put all bytes in one `PUT` with `Content-Length` = number of bytes in the file. The guide calls this "best because it requires fewer requests and results in better performance."
- *Multiple chunks:* "Create chunks in multiples of **256 KB (256 x 1024 bytes)** in size, except for the final chunk that completes the upload." Per chunk send:
  - `Content-Length` — number of bytes in the current chunk.
  - `Content-Range` — e.g. `Content-Range: bytes 0-524287/2000000` (first 524,288 bytes of a 2,000,000-byte file).
  - Use the response `Range` header to decide where the next chunk starts — "Don't assume that the server received all bytes sent in the previous request."
- `308 Resume Incomplete` means the chunk was received successfully and more data is expected; `200 OK` / `201 Created` means the upload is fully complete.

**Step 3 — resume an interrupted upload:** send an **empty `PUT`** to the session URI with `Content-Range: bytes */2000000` (or `*/*` if the total size is unknown), then:

| Response | Meaning |
| --- | --- |
| `200 OK` / `201 Created` | Upload already completed |
| `308 Resume Incomplete` | Continue uploading |
| `404 Not Found` | "the upload session has expired and the upload must be restarted from the beginning" |

Parse the response `Range` header — e.g. `Range: bytes=0-42` means the first 43 bytes were received and "the next chunk to upload would start with byte 44".

**Error handling (verbatim guidance):**

- `5xx` → "resume or retry uploads that fail due to connection interruptions."
- `403 rate limit` → "retry the upload."
- "For any `4xx` errors (including `403`) during a resumable upload, **restart the upload**. These errors indicate the upload session has expired and must be restarted by requesting a new session URI."

**`PATCH` vs `PUT`:** "Use the HTTP verb documented on the API reference for the initial request of a resumable upload or for the only request of a simple or multipart upload. Use `PUT` for all subsequent requests for a resumable upload once the request has started."

### 1.4 Metadata-only create (including folders)

```
POST https://www.googleapis.com/drive/v3/files
```

Guide: [Create metadata-only files](https://developers.google.com/workspace/drive/api/guides/create-file#create-metadata-file). "When you create the file, the method returns a `files` resource. The file is given a `kind` of `drive.file`, an `id`, a `name` of `"Untitled"`, and a `mimeType` of `application/octet-stream`." The guide notes `uploadType` "is marked as required but defaults to `media`, so you don't actually have to supply it."

**Folder** — [Create and populate folders](https://developers.google.com/workspace/drive/api/guides/folder#create-folder):

> "A folder is a file with the MIME type `application/vnd.google-apps.folder` and it has no extension."
> "The alias `root` can be used to refer to the root folder anywhere a file ID is provided."

```
POST https://www.googleapis.com/drive/v3/files
Content-Type: application/json

{
  "name": "My Folder",
  "mimeType": "application/vnd.google-apps.folder",
  "parents": ["FOLDER_ID"]
}
```

A shortcut is the other documented metadata-only example, with MIME type `application/vnd.google-apps.shortcut` ([Create a shortcut to a Drive file](https://developers.google.com/workspace/drive/api/guides/shortcuts)).

### 1.5 Creating a Google Docs / Sheets / Slides native type ("convert to Google Docs format")

Guide section: [Import to Google Docs types](https://developers.google.com/workspace/drive/api/guides/manage-uploads#import-docs).

**What it means:** you send the *source* bytes (e.g. a `.docx` or Markdown file) as the media part, but you set the `mimeType` in the **metadata** to the *Google Workspace* type. Drive then **imports** the source into that native format. The resulting file is a native Docs editors file — it has no binary content of its own, so you cannot download it with `alt=media`; you must use [`files.export`](#4-export-a-google-workspace-file-to-another-format).

Verbatim from the guide:

> "To convert a file to a specific Google Workspace file type, specify the Google Workspace `mimeType` when creating the file."

From the Discovery `File.mimeType` field description:

> "The MIME type of the file. Google Drive attempts to automatically detect an appropriate value from uploaded content, if no value is provided. **The value cannot be changed unless a new revision is uploaded. If a file is created with a Google Doc MIME type, the uploaded content is imported, if possible.** The supported import formats are published in the `about` resource."

**Native Google Workspace MIME types** ([supported MIME types](https://developers.google.com/workspace/drive/api/guides/mime-types)):

| MIME type | Description |
| --- | --- |
| `application/vnd.google-apps.document` | Google Docs |
| `application/vnd.google-apps.spreadsheet` | Google Sheets |
| `application/vnd.google-apps.presentation` | Google Slides |
| `application/vnd.google-apps.folder` | Google Drive folder |
| `application/vnd.google-apps.shortcut` | Google Drive shortcut |
| `application/vnd.google-apps.drawing` | Google Drawings |
| `application/vnd.google-apps.form` | Google Forms |
| `application/vnd.google-apps.script` | Google Apps Script |
| `application/vnd.google-apps.site` | Google Sites |
| `application/vnd.google-apps.drive-sdk` | Third-party shortcut |
| `application/vnd.google-apps.vid` | Google Vids |

**Supported imports** (guide table, "Some common import formats"):

| From | To |
| --- | --- |
| Microsoft Word, OpenDocument Text, HTML, RTF, plain text, **Markdown** | Google Docs |
| Microsoft Excel, OpenDocument Spreadsheet, CSV, TSV, plain text | Google Sheets |
| Microsoft PowerPoint, OpenDocument Presentation | Google Slides |
| JPEG, PNG, GIF, BMP, PDF | Google Docs (embeds the image in a Doc) |
| Plain text (special MIME type), JSON | Google Apps Script |

- **Authoritative runtime list:** "To see if a conversion is available, check the `importFormats` field of the `about` resource before creating the file. Supported conversions are available dynamically in this array." (`about.importFormats` is documented in Discovery as "A map of source MIME type to possible targets for all supported imports." Related: `about.maxImportSizes` — "A map of maximum import sizes by MIME type, in bytes.")
- **Replacement semantics on update:** "When you upload and convert media during an `update` request to a Docs, Sheets, or Slides file, the full contents of the document are replaced."
- **OCR:** "When you convert an image to a Docs, Drive uses Optical Character Recognition (OCR) to convert the image to text. You can improve the quality of the OCR algorithm by specifying the applicable [BCP 47](https://www.rfc-editor.org/info/bcp47) language code in the `ocrLanguage` parameter. The extracted text appears in the document alongside the embedded image."
  - ⚠️ **Discrepancy:** the Discovery document describes `ocrLanguage` as "A language hint for OCR processing during image import (**ISO 639-1** code)" while the guide says **BCP 47**. Simple codes like `en` satisfy both.
- **Pre-generated IDs are not supported for this:** "pre-generated IDs aren't supported for the creation of Google Workspace files, except for the `application/vnd.google-apps.drive-sdk` and `application/vnd.google-apps.folder` MIME types. Similarly, uploads referencing a conversion to a Google Workspace file format aren't supported."

### 1.6 Which metadata JSON fields the metadata part can contain

The request body of `files.create` / `files.copy` / `files.update` is the [`File`](https://developers.google.com/workspace/drive/api/reference/rest/v3/files) resource. The Discovery `File` schema has **many** properties; the ones that are meaningful to *write* on create are:

| Field | Type | Notes (from Discovery descriptions) |
| --- | --- | --- |
| `name` | string | "The name of the file. This isn't necessarily unique within a folder." |
| `mimeType` | string | See §1.5; "cannot be changed unless a new revision is uploaded." |
| `parents[]` | array of string | "A file can only have one parent folder; specifying multiple parents isn't supported. If not specified as part of a create request, the file is placed directly in the user's My Drive folder." |
| `description` | string | "A short description of the file." |
| `starred` | boolean | "Whether the user has starred the file." |
| `trashed` | boolean | Set via `files.update` to trash/untrash. |
| `properties` | object | "arbitrary key-value pairs which are **visible to all apps**." |
| `appProperties` | object | "arbitrary key-value pairs which are **private to the requesting app** … can only be retrieved using an authenticated request … You cannot use an API key to retrieve private properties." |
| `contentHints` | object | "Additional information about the content of the file. These fields are never populated in responses." Contains `contentHints.thumbnail` and `contentHints.indexableText`. |
| `contentRestrictions` | array | Restrictions for accessing file content. |
| `copyRequiresWriterPermission` | boolean | "Whether the options to copy, print, or download this file should be disabled for readers and commenters." |
| `writersCanShare` | boolean | "Whether users with only `writer` permission can modify the file's permissions." |
| `folderColorRgb` | string | "The color for a folder or a shortcut to a folder as an RGB hex string." Palette from `about.folderColorPalette`. |
| `originalFilename` | string | "The original filename of the uploaded content if available." |
| `shortcutDetails.targetId` | string | "Can only be set on `files.create` requests." |
| `labelInfo` | object | Label information on the file. |
| `id` | string | Only meaningful when set from `files.generateIds`. |
| `createdTime` / `modifiedTime` | string (RFC 3339) | Settable; "setting `modifiedTime` will also update `modifiedByMeTime` for the user." |

Fields marked **"Output only."** in the Discovery descriptions include `ownedByMe`, `webViewLink`, `webContentLink`, `iconLink`, `thumbnailLink`, `size`, `quotaBytesUsed`, `md5Checksum`, `sha1Checksum`, `sha256Checksum`, `headRevisionId`, `exportLinks`, `spaces`, `shared`, `driveId`, `fileExtension`, `fullFileExtension`, `hasThumbnail`, `version`, `capabilities`, `permissionIds`, `resourceKey`, `isAppAuthorized`, `explicitlyTrashed`, `trashedTime`, `owners`, `permissions`, `lastModifyingUser`, `imageMediaMetadata`, `videoMediaMetadata`, `shortcutDetails.targetResourceKey`, `shortcutDetails.targetMimeType`, `labelInfo`.

> "When creating files, they should specify a file extension in the file's `name` field. … Subsequent calls to the `get` method return the read-only `fileExtension` property containing the extension originally specified in the `name` field."

**Query parameters for `files.create`** (from Discovery): `ocrLanguage`, `includeLabels`, `supportsAllDrives` (default `false`), `enforceSingleParent` (**Deprecated**: "Creating files in multiple folders is no longer supported."), `ignoreDefaultVisibility`, `keepRevisionForever`, `includePermissionsForView` (only `published` supported), `useContentAsIndexableText`, `supportsTeamDrives` (**Deprecated**: use `supportsAllDrives`), plus the global `uploadType`, `fields`, `alt`, `key`.

**Scopes for `files.create`** (Discovery, exactly three): `…/auth/drive`, `…/auth/drive.appdata`, `…/auth/drive.file`.

### 1.7 Pre-generated IDs (`files.generateIds`)

```
GET https://www.googleapis.com/drive/v3/files/generateIds?count=10&space=drive
```

- `count` — "The number of IDs to return." Default `10`; "The maximum number of IDs you can request is capped at 1,000."
- `space` — Default `drive`; "Supported values are `drive` and `appDataFolder`."
- `type` — Default `files`; "Supported values are `files` and `shortcuts`."
- Pass the ID via the `id` field of `files.create` / `files.copy`. "If the file is successfully created or copied, subsequent retries return a `409 Conflict` HTTP status code response and duplicate files aren't created."
- Scopes (Discovery): `…/auth/drive`, `…/auth/drive.appdata`, `…/auth/drive.file`.

### 1.8 Ownership note

The create-file guide says: OAuth 2.0 user auth → that user becomes owner; **service account** auth → "the Service Account is the file owner" and "If the Service Account is deleted, all files it owns are deleted immediately." The folder guide instead states: "**service accounts can't own any files.** Instead, they must upload files and folders into shared drives, or use OAuth 2.0 to upload items on behalf of a human user." ⚠️ **These two official pages contradict each other** — flagging rather than resolving.

---

## 2. Get file metadata

Guide: [Search for files and folders → Get a file by ID](https://developers.google.com/workspace/drive/api/guides/search-files) · Reference: [`files.get`](https://developers.google.com/workspace/drive/api/reference/rest/v3/files/get)

```
GET https://www.googleapis.com/drive/v3/files/{fileId}
```

**Path parameter:** `fileId` (string, **required**) — "The ID of the file."

**Query parameters (complete list from Discovery):**

| Parameter | Type | Default | Description |
| --- | --- | --- | --- |
| `fields` | string | — | Global system parameter. "Selector specifying which fields to include in a partial response." |
| `supportsAllDrives` | boolean | `false` | "Whether the requesting application supports both My Drives and shared drives." |
| `includeLabels` | string | — | "A comma-separated list of IDs of labels to include in the `labelInfo` part of the response." |
| `includePermissionsForView` | string | — | "Specifies which additional view's permissions to include in the response. Only `published` is supported." |
| `acknowledgeAbuse` | boolean | `false` | "Whether the user is acknowledging the risk of downloading known malware or other abusive files. This is only applicable when the `alt` parameter is set to `media` and the user is the owner of the file or an organizer of the shared drive in which the file resides." |
| `supportsTeamDrives` | boolean | `false` | **Deprecated:** use `supportsAllDrives` instead. |

**`alt=media`:** the guide says "If you provide the `alt=media` parameter, then the response includes the file contents in the response body." `files.get` has `supportsMediaDownload: true` and `supportsSubscription: true` in Discovery.

**Default fields:** "If you omit the `fields` parameter, the server returns a default set of fields specific to the method. For example, the `list` method returns only the `kind`, `id`, `name`, `mimeType`, and `resourceKey` fields for each file."

**Realistic `fields` value** (every identifier below is a real `File` schema field taken from the Discovery document):

```
fields=id,name,mimeType,description,parents,trashed,starred,explicitlyTrashed,size,version,
  createdTime,modifiedTime,modifiedByMeTime,viewedByMeTime,sharedWithMeTime,
  webViewLink,webContentLink,iconLink,thumbnailLink,
  md5Checksum,sha1Checksum,sha256Checksum,fileExtension,fullFileExtension,originalFilename,
  owners(displayName,emailAddress,permissionId),lastModifyingUser(displayName,emailAddress),
  sharingUser(displayName,emailAddress),capabilities(canEdit,canComment,canShare,canDownload,canDelete,canTrash,canRename,canCopy,canMoveItemWithinDrive),
  shared,ownedByMe,driveId,isAppAuthorized,resourceKey,spaces,exportLinks,
  properties,appProperties,shortcutDetails(targetId,targetMimeType),
  contentRestrictions(readOnly,reason),copyRequiresWriterPermission,writersCanShare
```

Because `fields` is a partial-response selector, a compact form is often better, e.g.:
`fields=id,name,mimeType,parents,modifiedTime,size,webViewLink`

**Scopes for `files.get`** (Discovery, eight): `…/auth/drive`, `…/auth/drive.appdata`, `…/auth/drive.file`, `…/auth/drive.meet.readonly`, `…/auth/drive.metadata`, `…/auth/drive.metadata.readonly`, `…/auth/drive.photos.readonly`, `…/auth/drive.readonly`.

---

## 3. List / search files

Guides: [Search for files and folders](https://developers.google.com/workspace/drive/api/guides/search-files) · [Search query terms and operators](https://developers.google.com/workspace/drive/api/guides/ref-search-terms) · Reference: [`files.list`](https://developers.google.com/workspace/drive/api/reference/rest/v3/files/list)

```
GET https://www.googleapis.com/drive/v3/files
```

"Use the `list` method without any parameters to return all files and folders in the current user's My Drive."

### 3.1 Query string syntax

> "A query string contains the following three parts: `query_term operator values`"

**Operators (complete table from the reference page):**

| Operator | Usage |
| --- | --- |
| `contains` | The content of one string is present in the other. |
| `=` | The content of a string or boolean is equal to the other. |
| `!=` | The content of a string or boolean is not equal to the other. |
| `<` | A value is less than another. |
| `<=` | A value is less than or equal to another. |
| `>` | A value is greater than another. |
| `>=` | A value is greater than or equal to another. |
| `in` | An element is contained within a collection. |
| `and` | Return items that match both queries. |
| `or` | Return items that match either query. |
| `not` | Negates a search query. |
| `has` | A collection contains an element matching the parameters. |

**File-specific query terms (complete table):**

| Query term | Valid operators | Usage |
| --- | --- | --- |
| `name` | `contains`, `=`, `!=` | "Name of the file. Surround with single quotes (`'`). Escape single quotes in queries with `\'`, such as `'Valentine\'s Day'`." |
| `fullText` | `contains` | "Whether the `name`, `description`, `indexableText` properties, or text in the file's content or metadata of the file matches." |
| `mimeType` | `contains`, `=`, `!=` | "MIME type of the file. Surround with single quotes (`'`)." |
| `modifiedTime` | `<=`, `<`, `=`, `!=`, `>`, `>=` | "Date of the last file modification. [RFC 3339](http://tools.ietf.org/html/rfc3339) format, default time zone is UTC, such as `2012-06-04T12:00:00-08:00`. Fields of type `date` are not comparable to each other, only to constant dates." |
| `viewedByMeTime` | `<=`, `<`, `=`, `!=`, `>`, `>=` | "Date that the user last viewed a file." |
| `trashed` | `=`, `!=` | "Whether the file is in the trash or not. Can be either `true` or `false`." |
| `starred` | `=`, `!=` | "Whether the file is starred or not." |
| `parents` | `in` | "Whether the parents collection contains the specified ID." |
| `owners` | `in` | "Users who own the file." |
| `writers` | `in` | "Users or groups who have permission to modify the file." |
| `readers` | `in` | "Users or groups who have permission to read the file." |
| `sharedWithMe` | `=`, `!=` | "Files that are in the user's 'Shared with me' collection … Can be either `true` or `false`." |
| `createdTime` | `<=`, `<`, `=`, `!=`, `>`, `>=` | "Date when the file was created … **Supported in Drive API v3 only.**" |
| `properties` | `has` | "Public custom file properties." |
| `appProperties` | `has` | "Private custom file properties." |
| `visibility` | `=`, `!=` | "Valid values are `anyoneCanFind`, `anyoneWithLink`, `domainCanFind`, `domainWithLink`, and `limited`." |
| `shortcutDetails.targetId` | `=`, `!=` | "The ID of the item the shortcut points to." |

Additionally documented on the search-files guide: `labels` (`'labels/LABEL_ID' in labels`) and label field values (`labels/LABEL_ID.FIELD_ID = 'VALUE'`).

**Escaping.** Verbatim from the search guide:

> "You must also escape special characters in your file names to make sure the query works correctly. For example, if a filename contains both an apostrophe (`'`) and a backslash (`"\"`) character, use a backslash to escape them: `name contains 'quinn\'s paper\\essay'`."

**Matching semantics (important, often-missed):**

- "The `contains` operator only performs **prefix matching** for a `name` term. For example, suppose you have a name of `HelloWorld`. A query of `name contains 'Hello'` returns a result, but a query of `name contains 'World'` doesn't."
- "The `contains` operator only performs matching on **entire string tokens** for the `fullText` term. For example, if the full text of a document contains the string `HelloWorld`, only the query `fullText contains 'HelloWorld'` returns a result."
- "The `contains` operator matches a phrase if the right operand is surrounded by **double quotes**." Examples given: `fullText contains '"Hello there"'` matches "Hello there world"; `fullText contains '"Hello world"'` does not. An underscore in the document is treated as a space, so `fullText contains '"Hello world"'` matches a document containing `Hello_world`.

**Combining and precedence.** Documented example: `modifiedTime > '2012-06-04T12:00:00' and (mimeType contains 'image/' or mimeType contains 'video/')` — parentheses group `or` sub-clauses. Negation: `not name contains 'hello'`, `not fullText contains 'hello'`.

**Custom property syntax.** `properties has { key='mass' and value='1.3kg' }`, `appProperties has { key='additionalID' and value='8e8aceg2af2ge72e78' }`, and key-only matching `properties has { key='department' }`.

### 3.2 Concrete example queries

1. **Everything in a folder, not trashed** (the canonical folder-listing query, used verbatim in the guide and in the public-folder sample):
   ```
   q='FOLDER_ID' in parents and trashed = false
   ```
2. **All Google Docs modified in 2023, newest first:**
   ```
   q=mimeType = 'application/vnd.google-apps.document' and modifiedTime > '2023-01-01T00:00:00' and trashed = false
   orderBy=modifiedTime desc
   ```
3. **Folders or shortcuts with "Project" in the name:**
   ```
   q=name contains 'Project' and (mimeType = 'application/vnd.google-apps.folder' or mimeType = 'application/vnd.google-apps.shortcut')
   ```
4. **Files by owner, excluding trashed, with a full-text phrase search:**
   ```
   q='test@example.org' in owners and fullText contains '"quarterly report"' and trashed = false
   ```
5. **Files tagged with an app-private property:**
   ```
   q=appProperties has { key='additionalID' and value='8e8aceg2af2ge72e78' } and trashed = false
   ```
6. **Files shared with me matching a name:**
   ```
   q=sharedWithMe and name contains 'hello'
   ```
7. **Escaped apostrophe and backslash in a name:**
   ```
   q=name contains 'quinn\'s paper\\essay'
   ```

Full request form:

```
GET https://www.googleapis.com/drive/v3/files
  ?q='FOLDER_ID'+in+parents+and+trashed+%3D+false
  &orderBy=modifiedTime+desc
  &pageSize=100
  &fields=nextPageToken,files(id,name,mimeType,modifiedTime,size)
  &supportsAllDrives=true
  &includeItemsFromAllDrives=true
```

### 3.3 List parameters (complete, from Discovery)

| Parameter | Type | Default | Notes |
| --- | --- | --- | --- |
| `q` | string | — | "A query for filtering the file results." |
| `pageSize` | integer | `100` | min `1`, max `1000`. "The service may return fewer than this value. If unspecified, at most 100 files will be returned for shared drives, and the entire list of files for non-shared drives. The maximum value is 1000; values above 1000 will be coerced to 1000." |
| `pageToken` | string | — | "The token for continuing a previous list request on the next page. This should be set to the value of `nextPageToken` from the previous response." |
| `orderBy` | string | — | See below. |
| `spaces` | string | `drive` | "A comma-separated list of spaces to query within the corpora. **Supported values are `drive` and `appDataFolder`.**" |
| `corpora` | string | — | "Supported items include: `user`, `domain`, `drive`, `allDrives`. Prefer `user` or `drive` to `allDrives` for efficiency. **By default, corpora is set to `user`.** However, this can change depending on the filter set through the `q` parameter." |
| `driveId` | string | — | "ID of the shared drive to search." |
| `includeItemsFromAllDrives` | boolean | `false` | "Whether both My Drive and shared drive items should be included in results." |
| `supportsAllDrives` | boolean | `false` | "Whether the requesting application supports both My Drives and shared drives." |
| `includeLabels` | string | — | Comma-separated label IDs to include in `labelInfo`. |
| `includePermissionsForView` | string | — | Only `published` is supported. |
| `corpus` | string | — | **Deprecated.** Enum: `domain`, `user`. "Use `corpora` instead." |
| `teamDriveId` | string | — | **Deprecated.** "Use `driveId` instead." |
| `includeTeamDriveItems` | boolean | `false` | **Deprecated.** "Use `includeItemsFromAllDrives` instead." |
| `supportsTeamDrives` | boolean | `false` | **Deprecated.** "Use `supportsAllDrives` instead." |

**`orderBy` — valid keys (complete list from Discovery):** `createdTime`, `folder`, `modifiedByMeTime`, `modifiedTime`, `name`, `name_natural`, `quotaBytesUsed`, `recency`, `sharedWithMeTime`, `starred`, `viewedByMeTime`. "Each key sorts ascending by default, but can be reversed with the `desc` modifier. Example usage: `?orderBy=folder,modifiedTime desc,name`."

Two official performance warnings:

- "Avoid using `createdTime` for queries on large item collections as it requires additional processing and it might result in timeouts or other issues. For time-related sorting on large item collections, you can use `modifiedTime` instead as it's optimized to handle these queries."
- "If you omit the `orderBy` query parameter, there's no default sort order and the items are returned arbitrarily."

Sort-collation detail from Discovery: `name` is "sorted using alphabetical ordering, so 1, 12, 2, 22"; `name_natural` uses natural sort ordering, "so 1, 2, 12, 22".

**Multi-corpora caveat:** "You can search multiple corpora in a single query; however, if the combined corpora is too large, the API might return incomplete results. Check the `incompleteSearch` field in the response body. If it's `true`, then some documents were omitted. To resolve this, narrow the `corpora` to use either `user` or `drive`."

**Shared drives:** to search inside a shared drive you must set `supportsAllDrives=true` and `includeItemsFromAllDrives=true` (guide, verbatim, in the public-folder sample).

**Public folders / API key:** "When listing files in a public folder, you can authenticate requests using an API key instead of OAuth 2.0 user credentials."

**Scopes for `files.list`** (Discovery, eight — same set as `files.get`): `…/auth/drive`, `…/auth/drive.appdata`, `…/auth/drive.file`, `…/auth/drive.meet.readonly`, `…/auth/drive.metadata`, `…/auth/drive.metadata.readonly`, `…/auth/drive.photos.readonly`, `…/auth/drive.readonly`.

---

## 4. Export a Google Workspace file to another format

Guides: [Download and export files](https://developers.google.com/workspace/drive/api/guides/manage-downloads) · [Export MIME types for Google Workspace documents](https://developers.google.com/workspace/drive/api/guides/ref-export-formats) · Reference: [`files.export`](https://developers.google.com/workspace/drive/api/reference/rest/v3/files/export)

```
GET https://www.googleapis.com/drive/v3/files/{fileId}/export?mimeType=application/pdf
```

**Parameters:**

| Parameter | Location | Required | Notes |
| --- | --- | --- | --- |
| `fileId` | path | **Yes** | "The ID of the file." |
| `mimeType` | query | **Yes** | "Required. The MIME type of the format requested for this export. For a list of supported MIME types, see Export MIME types for Google Workspace documents." |

**10 MB limit — confirmed.** The download guide states verbatim: "**Exported content is limited to 10 MB.**" (No equivalent byte limit is stated for `alt=media` binary downloads.)

**Scopes for `files.export`** (Discovery, exactly four): `…/auth/drive`, `…/auth/drive.file`, `…/auth/drive.meet.readonly`, `…/auth/drive.readonly`.
Note what is **absent**: `drive.metadata.readonly` and `drive.metadata` are **not** accepted by `files.export`.

### 4.1 Valid export MIME types (complete table from the reference page)

**Google Docs (Documents):**

| Format | MIME type | Extension |
| --- | --- | --- |
| Microsoft Word | `application/vnd.openxmlformats-officedocument.wordprocessingml.document` | `.docx` |
| OpenDocument | `application/vnd.oasis.opendocument.text` | `.odt` |
| Rich Text | `application/rtf` | `.rtf` |
| PDF | `application/pdf` | `.pdf` |
| Plain Text | `text/plain` | `.txt` |
| Web Page (HTML) | `text/html` | `.html` |
| Web Page (HTML, zipped) | `application/zip` | `.zip` |
| EPUB | `application/epub+zip` | `.epub` |
| **Markdown** | **`text/markdown`** | **`.md`** |

✅ **`text/markdown` for Google Docs IS supported** — it appears in the official export table.

**Google Sheets (Spreadsheets):**

| Format | MIME type | Extension |
| --- | --- | --- |
| Microsoft Excel | `application/vnd.openxmlformats-officedocument.spreadsheetml.sheet` | `.xlsx` |
| OpenDocument | `application/vnd.oasis.opendocument.spreadsheet` | `.ods` |
| PDF | `application/pdf` | `.pdf` |
| Web Page (HTML, zipped) | `application/zip` | `.zip` |
| Comma Separated Values (**first-sheet only**) | `text/csv` | `.csv` |
| Tab Separated Values (**first-sheet only**) | `text/tab-separated-values` | `.tsv` |

**Google Slides (Presentations):**

| Format | MIME type | Extension |
| --- | --- | --- |
| Microsoft PowerPoint | `application/vnd.openxmlformats-officedocument.presentationml.presentation` | `.pptx` |
| ODP | `application/vnd.oasis.opendocument.presentation` | `.odp` |
| PDF | `application/pdf` | `.pdf` |
| Plain Text | `text/plain` | `.txt` |

**Google Drawings:** `application/pdf` `.pdf`, `image/jpeg` `.jpg`, `image/png` `.png`, `image/svg+xml` `.svg`.
**Apps Script:** `application/vnd.google-apps.script+json` `.json`.
**Google Vids:** `video/mp4` `.mp4` — but see the note below; Vids cannot be exported via `files.export`.

**Runtime discovery of export formats:** "To view a list of all system supported export formats for a user, use the `get` method on the `about` resource with the `fields` parameter set to `exportFormats`." (Discovery: `about.exportFormats` = "A map of source MIME type to possible targets for all supported exports.")

### 4.2 `/export` vs `/download` vs `alt=media` — the distinction

| Target | Method + URL | Use for |
| --- | --- | --- |
| **Google Workspace (Docs editors) files** | `GET https://www.googleapis.com/drive/v3/files/{fileId}/export?mimeType=…` | Native Docs/Sheets/Slides/Drawings files. **Required**, because these files have no binary content. |
| **Blob (binary) files** | `GET https://www.googleapis.com/drive/v3/files/{fileId}?alt=media` | "any raw binary file stored on Google Drive (such as images, videos, and PDFs) as opposed to a Google Workspace document." |
| **Blob file, earlier revision** | `GET https://www.googleapis.com/drive/v3/files/{fileId}/revisions/{revisionId}?alt=media` | "You can only download blob file content revisions that are marked as 'Keep Forever'." |
| **Browser link (binary)** | `GET …/files/{fileId}?fields=webContentLink` | Redirect or link the user instead of streaming through your app. |
| **Browser link (Workspace)** | `GET …/files/{fileId}?fields=exportLinks` | "a link to download the file and its contents is returned for every MIME type available." |
| **Long-running-operation download** | `POST https://www.googleapis.com/drive/v3/files/{fileId}/download` with optional `mimeType` and `revisionId` | "This is the only way to download Google Vids files." Returns an LRO; poll it to get a content URL. |

⚠️ **Important:** "If you attempt to export Google Vids files, you receive a `fileNotExportable` error."

**Partial download:** use the HTTP `Range` header, e.g. `Range: bytes=500-999`.

**Scope warning, verbatim from the download guide:** "File downloads started from your app must be authorized with a scope that allows read access to the file content. For example, an app using the `drive.readonly.metadata` scope isn't authorized to download the file contents."
⚠️ The guide writes `drive.readonly.metadata`, which is **not** a real scope string; the Discovery document's scope is `https://www.googleapis.com/auth/drive.metadata.readonly`. Treat this as an official typo; the intent (metadata-only scopes cannot download content) is consistent with the Discovery scope lists.

**Capability pre-check:** "Before you download or export file content, verify that users can download the file using the `capabilities.canDownload` field on the `files` resource."

---

## 5. Share / create a permission

Guide: [Share files, folders, and drives](https://developers.google.com/workspace/drive/api/guides/manage-sharing) · References: [`permissions.create`](https://developers.google.com/workspace/drive/api/reference/rest/v3/permissions/create), [`permissions.update`](https://developers.google.com/workspace/drive/api/reference/rest/v3/permissions/update)

### 5.1 Create

```
POST https://www.googleapis.com/drive/v3/files/{fileId}/permissions
Content-Type: application/json

{
  "role": "commenter",
  "type": "user",
  "emailAddress": "alex@altostrat.com"
}
```

**Path parameter:** `fileId` — "The ID of the file **or shared drive**."

**Request-body fields** (from the Discovery `Permission` schema and the guide):

| Field | Type | Notes |
| --- | --- | --- |
| `role` | string | "Supported values include: `owner`, `organizer`, `fileOrganizer`, `writer`, `commenter`, `reader`." |
| `type` | string | "Supported values include: `user`, `group`, `domain`, `anyone`. When creating a permission, if `type` is `user` or `group`, you must provide an `emailAddress` for the user or group. If `type` is `domain`, you must provide a `domain`. If `type` is `anyone`, no extra information is required." |
| `emailAddress` | string | Required for `type=user` or `type=group`. |
| `domain` | string | Required for `type=domain`. |
| `allowFileDiscovery` | boolean | "Whether the permission allows the file to be discovered through search. This is only applicable for permissions of type `domain` or `anyone`." |
| `expirationTime` | string (RFC 3339) | "They can only be set on user and group permissions; The time must be in the future; The time cannot be more than a year in the future." |
| `inheritedPermissionsDisabled` | boolean | "When `true`, only organizers, owners, and users with permissions added directly on the item can access it." |
| `pendingOwner` | boolean | "Whether the account associated with this permission is a pending owner. Only populated for permissions of type `user` for files that aren't in a shared drive." |
| `view` | string | "Only populated for permissions that belong to a view. The only supported values are `published` and `metadata`." |

⚠️ **Observed discrepancy (flagging, not resolving).** In the Discovery `Permission` schema, `emailAddress` and `domain` carry `"readOnly": true`, yet their own descriptions and the guide both instruct you to *provide* them when creating a permission. The guides and the API's actual behavior treat them as writable. Do not rely on the `readOnly` flag here.

### 5.2 `permissions.create` query parameters (complete, from Discovery)

| Parameter | Type | Default | Notes |
| --- | --- | --- | --- |
| `sendNotificationEmail` | boolean | — | "Whether to send a notification email when sharing to users or groups. This defaults to `true` for users and groups, and **is not allowed for other requests**. It must not be disabled for ownership transfers." |
| `emailMessage` | string | — | "A plain text custom message to include in the notification email." |
| `transferOwnership` | boolean | `false` | "Whether to transfer ownership to the specified user and downgrade the current owner to a writer. **This parameter is required as an acknowledgement of the side effect.**" |
| `moveToNewOwnersRoot` | boolean | `false` | "This parameter only takes effect if the item isn't in a shared drive and the request is attempting to transfer the ownership of the item. If set to `true`, the item is moved to the new owner's My Drive root folder and all prior parents removed. If set to `false`, parents aren't changed." |
| `supportsAllDrives` | boolean | `false` | "Whether the requesting application supports both My Drives and shared drives." |
| `useDomainAdminAccess` | boolean | `false` | "Issue the request as a domain administrator. … 1. The file ID parameter refers to a shared drive. 2. The requester is an administrator of the domain to which the shared drive belongs." |
| `enforceSingleParent` | boolean | `false` | **Deprecated:** "See `moveToNewOwnersRoot` for details." |
| `enforceExpansiveAccess` | boolean | `false` | **Deprecated:** "All requests use the expansive access rules." |
| `supportsTeamDrives` | boolean | `false` | **Deprecated:** use `supportsAllDrives` instead. |

✅ **Correction to a common misconception:** `sendNotificationEmail`, `emailMessage`, `transferOwnership` and `moveToNewOwnersRoot` are **query parameters**, not request-body fields, on `permissions.create`.

### 5.3 Update (separate path)

```
PATCH https://www.googleapis.com/drive/v3/files/{fileId}/permissions/{permissionId}
Content-Type: application/json

{ "role": "writer" }
```

**Path parameters:** `fileId` — "The ID of the file or shared drive."; `permissionId` — "The ID of the permission."

**Query parameters (complete, from Discovery):** `supportsAllDrives` (default `false`), `useDomainAdminAccess` (default `false`), `transferOwnership` (default `false`; "required as an acknowledgement of the side effect"), `removeExpiration` (default `false`; "Whether to remove the expiration date."), `enforceExpansiveAccess` (**Deprecated**), `supportsTeamDrives` (**Deprecated**).

**Body:** the `Permission` resource — primarily `role`, or `allowFileDiscovery` when `type` is `domain` or `anyone`. The guide: "To make a shared file searchable or unlisted, include the `allowFileDiscovery` boolean field in your patch request. … You don't need to delete and recreate the permission to change this setting."

**Scopes (both create and update, Discovery, exactly two):** `https://www.googleapis.com/auth/drive`, `https://www.googleapis.com/auth/drive.file`.
Note: `drive.readonly`, `drive.metadata`, and `drive.metadata.readonly` **cannot** create or update permissions.

### 5.4 Other sharing facts

- **Deleting a permission:** `DELETE https://www.googleapis.com/drive/v3/files/{fileId}/permissions/{permissionId}` — same two scopes. "Inherited permissions cannot be revoked directly on child items."
- **Listing:** `GET https://www.googleapis.com/drive/v3/files/{fileId}/permissions` with `pageSize`, `pageToken`, `supportsAllDrives`, `useDomainAdminAccess`, `includePermissionsForView` (only `published`), `fields`. "By default, `list` returns only `id`, `type`, `kind`, and `role`."
- **Target audiences:** "set `type=domain` and set `domain` to `<TARGET_AUDIENCE_ID>.audience.googledomains.com`."
- **Concurrency:** "Concurrent permission modifications on the same file, folder, or shared drive aren't supported. … Simultaneous operations cause race conditions where 'last write wins,' which can silently overwrite permission changes or trigger `sharingRateLimitExceeded` errors."
- **Capabilities:** prefer `files.capabilities` (`canShare`, `canComment`, `canDelete`, …) over parsing permissions when rendering UI. Fetch with `files.get` and `fields=capabilities`.

---

## 6. Delete / trash a file

Guide: [Trash or delete files and folders](https://developers.google.com/workspace/drive/api/guides/delete)

### 6.1 Permanent delete

```
DELETE https://www.googleapis.com/drive/v3/files/{fileId}
```

- **Response:** empty JSON object.
- **Query parameters (Discovery):** `supportsAllDrives` (default `false`), `enforceSingleParent` (**Deprecated**), `supportsTeamDrives` (**Deprecated**).
- "To delete a shared drive file, you must also set the boolean `supportsAllDrives` query parameter to `true`."
- "To delete a shared drive file, the user must have `role=organizer` on the parent folder. If you're deleting a folder, all descendants owned by the user are also deleted."
- **Scopes (Discovery):** `…/auth/drive`, `…/auth/drive.appdata`, `…/auth/drive.file`.

### 6.2 Trash (and untrash) via `files.update`

```
PATCH https://www.googleapis.com/drive/v3/files/{fileId}
Content-Type: application/json

{ "trashed": true }
```

- Untrash with `{ "trashed": false }`.
- "To trash a shared drive file, you must also set the boolean `supportsAllDrives` query parameter to `true`."
- **Only the owner can trash:** "Only the file owner can trash a file, but other users can still access the file in the owner's trash until it's permanently deleted. If you attempt to trash a file you don't own, you receive an `insufficientFilePermissions` error."
- **Verify ownership first:** `files.get` with `fields=ownedByMe`. "The `ownedByMe` field isn't populated for files in shared drives because they're owned by the shared drive, not individual users."
- **30-day retention:** "Files in the trash are automatically deleted after 30 days."
- **Trash-state fields:** `trashed` and `explicitlyTrashed` are "populated for all files"; `trashedTime` and `trashingUser` are "only populated for files located within a shared drive".
- **Scopes for `files.update` (Discovery):** `…/auth/drive`, `…/auth/drive.appdata`, `…/auth/drive.file`, `…/auth/drive.metadata`, `…/auth/drive.scripts`.

### 6.3 Empty trash

```
DELETE https://www.googleapis.com/drive/v3/files/trash
```

(Note the flat path: `files/trash`, **not** `files/{fileId}/trash`.)

- **Query parameters:** `driveId` — "If set, empties the trash of the provided shared drive." and `enforceSingleParent` (**Deprecated**).
- **Response:** empty JSON object.
- ⚠️ **Scope: this method accepts exactly ONE scope — `https://www.googleapis.com/auth/drive`.** It does **not** accept `drive.file`, `drive.readonly`, or `drive.metadata.readonly`. This is the single most likely scope surprise in the whole list.

---

## 7. Copy a file

Guides: [Copy an existing file](https://developers.google.com/workspace/drive/api/guides/create-file#copy-an-existing-file) · Reference: [`files.copy`](https://developers.google.com/workspace/drive/api/reference/rest/v3/files/copy)

```
POST https://www.googleapis.com/drive/v3/files/{fileId}/copy
Content-Type: application/json

{
  "name": "FILE_COPY_NAME",
  "parents": ["DESTINATION_FOLDER_ID"],
  "description": "…"
}
```

**Path parameter:** `fileId` (required).

**Request body** — same `File` resource as `files.create` (see §1.6). Fields most relevant to copy: `name`, `parents[]`, `mimeType` (to convert on copy), `description`, `starred`, `properties`, `appProperties`, `copyRequiresWriterPermission`, `writersCanShare`, `folderColorRgb`, `createdTime`, `modifiedTime`, and `id` (from `files.generateIds`).

> **Converting on copy:** `mimeType` in the copy body is the documented mechanism for converting a *blob* file into a Google Workspace type (e.g. copy a `.docx` blob with `"mimeType": "application/vnd.google-apps.document"`), or converting between Workspace types where an export/import path exists. This follows from the Discovery `File.mimeType` description ("If a file is created with a Google Doc MIME type, the uploaded content is imported, if possible") plus the create-file guide's statement that `copy` accepts the same resource.

**Query parameters (complete, from Discovery):**

| Parameter | Type | Default | Notes |
| --- | --- | --- | --- |
| `supportsAllDrives` | boolean | `false` | "Whether the requesting application supports both My Drives and shared drives." |
| `enforceSingleParent` | boolean | `false` | **Deprecated:** "Copying files into multiple folders is no longer supported. Use shortcuts instead." |
| `ignoreDefaultVisibility` | boolean | `false` | "Whether to ignore the domain's default visibility settings for the created file. … Permissions are still inherited from parent folders." |
| `keepRevisionForever` | boolean | `false` | "Whether to set the `keepForever` field in the new head revision. This is only applicable to files with binary content in Google Drive. Only 200 revisions for the file can be kept forever." |
| `ocrLanguage` | string | — | "A language hint for OCR processing during image import (ISO 639-1 code)." |
| `copyComments` | boolean | `false` | "Whether to copy the comments associated with the file." |
| `includeLabels` | string | — | Comma-separated label IDs to include in `labelInfo`. |
| `includePermissionsForView` | string | — | Only `published` is supported. |
| `supportsTeamDrives` | boolean | `false` | **Deprecated:** use `supportsAllDrives` instead. |
| `fields` | string | — | Global system parameter (the user-requested query param). |

**`copyComments` detail:** "To copy comments and suggestions when you copy a Google Docs, Sheets, or Slides file, set the `copyComments` query parameter to `true`. Drive copies only open comments (unresolved comments). For other file types, Drive ignores this parameter." A separate `copyComments` **body** field also exists on `File` in the Discovery schema.

**Scopes for `files.copy`** (Discovery, four): `…/auth/drive`, `…/auth/drive.appdata`, `…/auth/drive.file`, `…/auth/drive.photos.readonly`.

**Documented limits and considerations:**

- "You can only copy a file into one parent folder. Specifying multiple parents isn't supported. If the `parents` field isn't specified, the file inherits any discoverable parents from the source file."
- "Even though a folder is a type of file, **you can't copy a folder.** Instead, create a destination folder and set the `parents` field of the existing files to the destination folder."
- "Unless a new filename is specified, the `copy` method produces a file with the same name as the original."
- "The user that created the copy owns the copied file. No other sharing settings from the source file are replicated. If the copy is created in a shared folder, it inherits the permissions of that folder."
- "Some files, like third-party shortcuts, can never be copied."
- "Excessive use of `copy` can lead to exceeding your Drive API quota limits."
- "The `DownloadRestrictionsMetadata` object of the `files` resource determines who can copy the file" and "The `capabilities.canCopy` field … determines whether the user can copy a file."

---

## 8. Scope summary and practical guidance

### 8.1 Scope × action matrix (extracted from Discovery `methods[].scopes`)

`YES` = the scope is accepted for that method. `.` = not accepted.

| Action | Method | `drive` | `drive.file` | `drive.readonly` | `drive.metadata.readonly` | `drive.appdata` | `drive.metadata` |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 1. Upload/create | `files.create` | ✅ | ✅ | — | — | ✅ | — |
| 1b. Create folder | `files.create` | ✅ | ✅ | — | — | ✅ | — |
| 2. Get metadata | `files.get` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| 3. List/search | `files.list` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| 4. Export Workspace file | `files.export` | ✅ | ✅ | ✅ | **—** | — | **—** |
| 5. Create permission | `permissions.create` | ✅ | ✅ | **—** | **—** | — | **—** |
| 5b. Update permission | `permissions.update` | ✅ | ✅ | — | — | — | — |
| 6. Delete permanently | `files.delete` | ✅ | ✅ | — | — | ✅ | — |
| 6b. Trash (`files.update`) | `files.update` | ✅ | ✅ | — | — | ✅ | ✅ |
| 6c. **`files.emptyTrash`** | `files.emptyTrash` | ✅ | **—** | **—** | **—** | — | — |
| 7. Copy | `files.copy` | ✅ | ✅ | — | — | ✅ | — |
| (ref) `changes.list` | `changes.list` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| (ref) `changes.watch` | `changes.watch` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |

**Key takeaways:**

- **`drive.file` is accepted by every action in this report except `files.emptyTrash`.** Google states this explicitly: "The `drive.file` scope works with all [Drive API REST Resources](https://developers.google.com/workspace/drive/api/reference/rest/v3) which means you can use it the same way you use broader OAuth scopes."
- **`drive.readonly` cannot write anything.** No create, no permission changes, no delete/trash, no copy. Export and download only.
- **`drive.metadata.readonly` cannot export or download content**, and cannot write anything. Metadata (including `files.list` and `files.get`) only.
- **`files.emptyTrash` is `drive`-only.** If your service needs "empty trash", `drive.file` is not sufficient.

### 8.2 The `drive.file` scope — exactly what the official docs say

**Official description #1** — the canonical one-line scope string, from the [OAuth 2.0 Scopes for Google APIs](https://developers.google.com/identity/protocols/oauth2/scopes) reference (Google Drive API, v3 section), verbatim:

> `https://www.googleapis.com/auth/drive.file` — "**See, edit, create, and delete only the specific Google Drive files you use with this app**"

That phrase — "**only the specific Google Drive files you use with this app**" — is the authoritative statement of the limitation, and it is what answers the listing question below: the grant is per-file, not per-Drive.

**Official description #2** — the Drive-specific expansion, from [Choose Google Drive API scopes](https://developers.google.com/workspace/drive/api/guides/api-specific-auth), "Non-sensitive scopes" table, verbatim:

> `https://www.googleapis.com/auth/drive.file` — "Create new Drive files, or modify existing files, that you open with an app or that the user shares with an app while using the Google Picker API or the app's file picker."

It is classified **non-sensitive**, which is the whole point: "Non-sensitive: These scopes provide the smallest scope of authorization and only require basic OAuth App Verification."

**For contrast, the same reference page (`drive` v3 section) defines the other scopes verbatim as:**

| Scope | Official one-line description |
| --- | --- |
| `https://www.googleapis.com/auth/drive` | "See, edit, create, and delete **all** of your Google Drive files" |
| `https://www.googleapis.com/auth/drive.readonly` | "See and download **all** your Google Drive files" |
| `https://www.googleapis.com/auth/drive.metadata` | "View and manage metadata of files in your Google Drive" |
| `https://www.googleapis.com/auth/drive.metadata.readonly` | "See information about your Google Drive files" |
| `https://www.googleapis.com/auth/drive.appdata` | "See, create, and delete its own configuration data in your Google Drive" |
| `https://www.googleapis.com/auth/drive.meet.readonly` | "See and download your Google Drive files that were created or edited by Google Meet." |

**Limitations and behavior, from the same page:**

> "The `drive.file` OAuth scope lets users choose which files they want to share with your app. This gives them more control and confidence that your app's access to their files is limited and more secure. In contrast, requiring broad access to all Drive files could discourage users from interacting with your app."

> "**Usability**: The `drive.file` scope works with all Drive API REST Resources which means you can use it the same way you use broader OAuth scopes."

> "**Convenience**: Apps can apply filters for certain Drive file types (such as Google Docs, Sheets, and photos) when using a filter on Google Picker files."

> "**Straightforward verification**: Since `drive.file` is non-sensitive, it allows for a more streamlined verification process."

> "Many apps can transition to per-file access without any changes. If you're using your own file picker, we recommend switching to the Google Picker API which fully supports different scopes."

**Answers to the two specific questions asked:**

1. **Does `drive.file` allow listing/searching all files?**
   **No.** The canonical scope description is "See, edit, create, and delete **only the specific Google Drive files you use with this app**" ([OAuth 2.0 Scopes for Google APIs](https://developers.google.com/identity/protocols/oauth2/scopes)), expanded by the Drive guide as files the app creates, files "you open with an app", and files "the user shares with an app while using the Google Picker API or the app's file picker" ([Choose Google Drive API scopes](https://developers.google.com/workspace/drive/api/guides/api-specific-auth)).
   `files.list` and `files.get` are **technically accepted** under `drive.file` (confirmed by the Discovery scope list, and by "The `drive.file` scope works with all Drive API REST Resources"), but the result set is constrained to that per-file grant. **Caveat on precision:** I found no single official sentence of the form "`files.list` under `drive.file` returns only app-created or app-opened files." The limitation is stated at the *scope* level, not restated as an endpoint-level behavior. The engineering conclusion — you cannot enumerate a user's whole Drive with `drive.file` — is sound and directly supported by the scope's own definition, but the endpoint-level restatement is inferred.

2. **Does `drive.file` work with `changes.watch`?**
   **Yes, mechanically.** The Discovery document lists `https://www.googleapis.com/auth/drive.file` among the accepted scopes for `drive.changes.watch`, `drive.changes.list`, and `drive.changes.getStartPageToken`. That means the change-log endpoints will accept the scope. Whether the change feed is *useful* under `drive.file` is the same per-file question as above: a change log scoped to per-file access cannot substitute for a whole-Drive change feed. **I could not find an official guide sentence that documents a `drive.file`-specific restriction on the change log.** The mechanical scope acceptance is confirmed; the semantic limitation is inferred.

3. **Scope strings for the change log (for reference, from Discovery):** `changes.watch` accepts eight scopes — `drive`, `drive.appdata`, `drive.file`, `drive.meet.readonly`, `drive.metadata`, `drive.metadata.readonly`, `drive.photos.readonly`, `drive.readonly`. Note that `changes.getStartPageToken` is `GET https://www.googleapis.com/drive/v3/changes/startPageToken` and `changes.watch` is `POST https://www.googleapis.com/drive/v3/changes/watch`. Also officially noted: "While not documented in `changes.watch`, the method does require a `pageToken` parameter similar to `changes.list`. This can be retrieved from `changes.getStartPageToken`."

### 8.3 Restricted scopes (`drive`, `drive.readonly`, `drive.metadata.readonly`)

Verbatim scope descriptions from [Choose Google Drive API scopes](https://developers.google.com/workspace/drive/api/guides/api-specific-auth), "Restricted scopes" table:

| Scope | Official description |
| --- | --- |
| `https://www.googleapis.com/auth/drive` | "View and manage all your Drive files." |
| `https://www.googleapis.com/auth/drive.readonly` | "View and download all your Drive files." |
| `https://www.googleapis.com/auth/drive.metadata` | "View and manage metadata of files in your Drive." |
| `https://www.googleapis.com/auth/drive.metadata.readonly` | "View metadata for files in your Drive." |

> "**Restricted**: These scopes provide wide access to Google user data and require restricted scope OAuth App Verification. … **If you store restricted scope data on servers (or transmit), then you must go through a security assessment.**"

> "**Qualifications for restricted scopes.** Only specific application types are permitted to use restricted scopes for Google Drive. To qualify, your app must fall into one of the following categories: 1. **Backup and sync** … 2. **Productivity and education** … 3. **Reporting and security** …"

This is the strongest practical argument for `drive.file`: using `drive`/`drive.readonly`/`drive.metadata.readonly` puts the app into restricted-scope verification and a security assessment if data is stored server-side.

### 8.4 Incremental authorization

The Drive auth guide establishes the two-place scope model, verbatim:

> "To authorize your app, the Google Drive API requires you to define OAuth scopes in two places: the Google Cloud console and your app."
> "In the Google Cloud console, you must declare the scopes your app needs in its OAuth consent screen configuration. These are the highest level of permissions your app can ever request."
> "In your app, when you initiate the API, you must explicitly request the specific scopes you need for that session. While the Google Cloud console defines the highest level of permissions your app is allowed to request, the code determines the actual permissions for a given user. This helps make sure the app only asks for the permissions needed for a specific task."

The guide then demonstrates declaring multiple scopes as an array, e.g. in the Java sample `DriveScopes.DRIVE_FILE, DriveScopes.DRIVE_METADATA_READONLY`, and in Python `["https://www.googleapis.com/auth/drive.file", "https://www.googleapis.com/auth/drive.metadata.readonly"]`.

> **Note on what the Drive page does and does not say:** the Drive auth guide documents *requesting a narrow scope set per session* and *mixing a non-sensitive scope with a restricted one*. It does **not** itself use the term "incremental authorization" or name the OAuth parameter used to request additional scopes on a later consent. **Incremental authorization is documented in the Google Identity OAuth 2.0 docs, not in the Drive guide set.**

**The actual mechanism** — from [Using OAuth 2.0 to Access Google APIs](https://developers.google.com/identity/protocols/oauth2) / [OAuth 2.0 for Web Server Applications](https://developers.google.com/identity/protocols/oauth2/web-server), verbatim:

> "We also recommend that your application request access to authorization scopes via an incremental authorization process, in which your application requests access to user data in context. This best practice helps users to more easily understand why your application needs the access it is requesting."

> "We recommend that your application request access to authorization scopes in context whenever possible. By requesting access to user data in context, using incremental authorization, you help users to understand why your application needs the access it is requesting."

The authorization-request parameter, verbatim from the same page's parameter table:

> **`include_granted_scopes`** | Optional | "Enables applications to use incremental authorization to request access to additional scopes in context. If you set this parameter's value to `true` and the authorization request is granted, then the new access token will also cover any scopes to which the user previously granted the application access. See the incremental authorization section for examples."

So the flow is: send the user through the authorization endpoint with the scopes you need *now* plus `include_granted_scopes=true`; previously granted scopes are carried into the new access token, and later sessions can request additional scopes in context without re-prompting for everything.

**Practical recommended shape for a Go service:**

1. Declare in the OAuth consent screen only the maximum set you may ever need.
2. Request the **narrowest** scope per session at runtime; prefer `drive.file` (non-sensitive → no security assessment) and add `drive.metadata.readonly` only if you genuinely need cross-Drive metadata visibility.
3. If you must have `files.emptyTrash`, you need the full `drive` scope — design around it or drop the feature.
4. Store refresh tokens securely ("Save refresh tokens in secure, long-term storage and continue to use them as long as they remain valid.").

---

## 9. Could NOT confirm

1. **`drive.file` + `files.list` endpoint-level semantics.** Confirmed at the *scope* level: `drive.file` = "See, edit, create, and delete only the specific Google Drive files you use with this app" ([scopes reference](https://developers.google.com/identity/protocols/oauth2/scopes)). **Not** confirmed: an official sentence stating that `files.list`/`files.get` under `drive.file` return *only* app-created/app-opened files. The endpoint accepts the scope; the restriction is documented at the scope level, not the endpoint level.
2. **`drive.file` + `changes.watch` semantics.** Scope acceptance is confirmed in the Discovery document. No official guide states whether the change feed is restricted to per-file-accessible items under `drive.file`. The guide pages I checked (`about-changes`, `about-changelogs`, `manage-changes`, `push`) do not discuss `drive.file` in relation to the change log.
3. **Incremental authorization is NOT documented in the Drive API guides.** The term appears nowhere in [Choose Google Drive API scopes](https://developers.google.com/workspace/drive/api/guides/api-specific-auth) or [Share files, folders, and drives](https://developers.google.com/workspace/drive/api/guides/manage-sharing) — the two sources named for this topic. I confirmed the mechanism and the `include_granted_scopes` parameter from the Google Identity docs ([web server flow](https://developers.google.com/identity/protocols/oauth2/web-server)) instead. I did **not** verify the full worked example from the "incremental authorization" section of that page, nor any Go-specific sample.
4. **`files.copy` body-field semantics for `mimeType` conversion.** The `File.mimeType` description covers imports at create time, and `copy` accepts the `File` resource, but I found no official statement enumerating which source→target conversions are permitted specifically *on copy*. Use `about.importFormats` / `about.exportFormats` at runtime, and expect a `400` for unsupported pairs.
5. **`ocrLanguage` format.** Official docs conflict: the guide says BCP 47, the Discovery document says ISO 639-1. Simple two-letter codes satisfy both.
6. **Service-account file ownership.** Official docs conflict (see §1.8): the create-file guide says a service account is the file owner; the folder guide says service accounts *cannot* own files and must use shared drives or OAuth 2.0 on behalf of a user.
7. **`drive.readonly.metadata`.** The download guide uses this string, which is not a real scope. The real scope is `https://www.googleapis.com/auth/drive.metadata.readonly`. Treat it as a doc typo.
8. **`Permission.emailAddress` / `Permission.domain` `readOnly` flag.** The Discovery schema marks both `readOnly: true`, contradicting its own descriptions and the manage-sharing guide, which instruct you to supply them. Behaviorally they must be writable; the flag appears to be a Discovery-document defect.
9. **The delete guide's role-permission table.** The rendered table's checkmark cells did not survive text extraction, so I cannot quote which roles may trash vs. delete from that table. The prose statements I *did* capture are: only the owner may trash a file (My Drive), and deleting a shared-drive file requires `role=organizer` on the parent folder.
10. **Exact `fields` default sets.** The guides state that omitting `fields` returns a method-specific default and give the `list` example (`kind`, `id`, `name`, `mimeType`, `resourceKey`), but I did not find the documented default set for `files.get`.
11. **Whether `enforceSingleParent` on `files.copy` still has any effect.** The Discovery doc marks it Deprecated but retains it as a parameter.

---

## Appendix — Quick-reference endpoint table

| # | Action | Method + URL |
| --- | --- | --- |
| 1a | Simple upload | `POST https://www.googleapis.com/upload/drive/v3/files?uploadType=media` |
| 1b | Multipart upload | `POST https://www.googleapis.com/upload/drive/v3/files?uploadType=multipart` |
| 1c | Resumable session init | `POST https://www.googleapis.com/upload/drive/v3/files?uploadType=resumable` |
| 1d | Metadata-only create / folder | `POST https://www.googleapis.com/drive/v3/files` |
| 1e | Generate IDs | `GET https://www.googleapis.com/drive/v3/files/generateIds` |
| 1f | Update existing (incl. resumable init) | `PATCH https://www.googleapis.com/drive/v3/files/{fileId}` |
| 2 | Get metadata | `GET https://www.googleapis.com/drive/v3/files/{fileId}` |
| 3 | List / search | `GET https://www.googleapis.com/drive/v3/files` |
| 4a | Export Workspace file | `GET https://www.googleapis.com/drive/v3/files/{fileId}/export?mimeType={mimeType}` |
| 4b | Download binary | `GET https://www.googleapis.com/drive/v3/files/{fileId}?alt=media` |
| 4c | Download (LRO, Vids) | `POST https://www.googleapis.com/drive/v3/files/{fileId}/download` |
| 5a | Create permission | `POST https://www.googleapis.com/drive/v3/files/{fileId}/permissions` |
| 5b | Update permission | `PATCH https://www.googleapis.com/drive/v3/files/{fileId}/permissions/{permissionId}` |
| 5c | Delete permission | `DELETE https://www.googleapis.com/drive/v3/files/{fileId}/permissions/{permissionId}` |
| 6a | Permanent delete | `DELETE https://www.googleapis.com/drive/v3/files/{fileId}` |
| 6b | Trash / untrash | `PATCH https://www.googleapis.com/drive/v3/files/{fileId}` with `{"trashed": true|false}` |
| 6c | Empty trash | `DELETE https://www.googleapis.com/drive/v3/files/trash` |
| 7 | Copy | `POST https://www.googleapis.com/drive/v3/files/{fileId}/copy` |
| — | Changes list | `GET https://www.googleapis.com/drive/v3/changes` |
| — | Changes watch | `POST https://www.googleapis.com/drive/v3/changes/watch` |
| — | Start page token | `GET https://www.googleapis.com/drive/v3/changes/startPageToken` |
| — | About (import/export formats) | `GET https://www.googleapis.com/drive/v3/about` |
