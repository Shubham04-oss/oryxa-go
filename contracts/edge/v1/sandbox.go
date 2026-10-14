package v1

// The sandbox API (contract §5): Go's cfsandbox calls these on a SandboxDO,
// through the room, at SandboxPath(room, name) + the suffix on each type.

// Languages sandbox exec/code runs.
const (
	LanguagePython     = "python"
	LanguageJavaScript = "javascript"
	LanguageTypeScript = "typescript"
)

// ExecCodeRequest is POST …/exec/code. Code runs in the sandbox's kernel for
// its language, whose state (variables, imports) lasts from one call to the
// next (A12); ClearContext resets that state first. Files persist either
// way.
type ExecCodeRequest struct {
	Language     string `json:"language"`
	Code         string `json:"code"`
	TimeoutMs    int64  `json:"timeoutMs,omitempty"`
	ClearContext bool   `json:"clearContext,omitempty"`
}

// ExecCommandRequest is POST …/exec/command. Cwd defaults to the sandbox's
// working directory.
type ExecCommandRequest struct {
	Command   string `json:"command"`
	Cwd       string `json:"cwd,omitempty"`
	TimeoutMs int64  `json:"timeoutMs,omitempty"`
}

// ExecResult answers both exec calls. Stdout and stderr are capped; a cap
// sets OutputTruncated. TimedOut means the process was killed at its limit,
// and then ExitCode is -1. SessionRecreated means the container had to be
// started for this call, so earlier processes' state is gone (files on the
// workspace survive while the DO keeps its disk).
type ExecResult struct {
	Stdout           string       `json:"stdout"`
	Stderr           string       `json:"stderr"`
	ExitCode         int          `json:"exitCode"`
	Results          []ExecOutput `json:"results"`
	DurationMs       int64        `json:"durationMs"`
	OutputTruncated  bool         `json:"outputTruncated,omitempty"`
	TimedOut         bool         `json:"timedOut,omitempty"`
	SessionRecreated bool         `json:"sessionRecreated,omitempty"`
	ChangedFiles     []string     `json:"changedFiles,omitempty"`
}

// ExecOutput is one rich output of a run, such as a chart a script saved.
// Text holds text; Data holds base64 bytes.
type ExecOutput struct {
	Type     string `json:"type"`
	MIMEType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	Name     string `json:"name,omitempty"`
}

// FileEntry is one directory entry. Type is "file", "directory", "symlink"
// or "other"; Size is set for files.
type FileEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Type string `json:"type"`
	Size *int64 `json:"size,omitempty"`
}

// ListFilesResponse answers GET …/files?path=.
type ListFilesResponse struct {
	Path      string      `json:"path"`
	Entries   []FileEntry `json:"entries"`
	Truncated bool        `json:"truncated,omitempty"`
}

// ReadFilesRequest is POST …/files/read.
type ReadFilesRequest struct {
	Paths []string `json:"paths"`
}

// File encodings on the wire.
const (
	EncodingUTF8   = "utf8"
	EncodingBase64 = "base64"
)

// FileContent is one file read. Error is set, and Content empty, when that
// path couldn't be read; the other paths still are.
type FileContent struct {
	Path      string `json:"path"`
	Encoding  string `json:"encoding,omitempty"`
	Content   string `json:"content,omitempty"`
	MIMEType  string `json:"mimeType,omitempty"`
	Size      int64  `json:"size"`
	Truncated bool   `json:"truncated,omitempty"`
	Error     string `json:"error,omitempty"`
}

// ReadFilesResponse answers POST …/files/read.
type ReadFilesResponse struct {
	Files []FileContent `json:"files"`
}

// WriteFile is one file to write. Encoding defaults to utf8.
type WriteFile struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Encoding string `json:"encoding,omitempty"`
}

// WriteFilesRequest is POST …/files/write.
type WriteFilesRequest struct {
	Files []WriteFile `json:"files"`
}

// WrittenFile is one file written.
type WrittenFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// WriteFilesResponse answers POST …/files/write.
type WriteFilesResponse struct {
	Written []WrittenFile `json:"written"`
}

// RemoveFilesRequest is POST …/files/remove.
type RemoveFilesRequest struct {
	Paths []string `json:"paths"`
}

// RemoveFilesResponse answers POST …/files/remove.
type RemoveFilesResponse struct {
	Removed []string `json:"removed"`
}

// ResetResponse answers POST …/reset: the container is gone with its files.
type ResetResponse struct {
	Reset bool `json:"reset"`
}

// ArtifactRequest is POST …/artifact: copy a sandbox file into R2 under the
// run (artifacts/{name}). Name defaults to the file's base name.
type ArtifactRequest struct {
	Path     string `json:"path"`
	Name     string `json:"name,omitempty"`
	MIMEType string `json:"mimeType,omitempty"`
}

// ArtifactResponse is the saved artifact's reference.
type ArtifactResponse struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	MIMEType string `json:"mimeType"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
}

// PreviewRequest is POST …/preview.
type PreviewRequest struct {
	Port int `json:"port"`
}

// SignedURL is a short-lived URL the edge signed: a preview, a terminal or a
// browser live view. ExpiresAt is RFC 3339.
type SignedURL struct {
	URL       string `json:"url"`
	ExpiresAt string `json:"expiresAt"`
}

// TerminalRequest is POST …/terminal.
type TerminalRequest struct {
	Cols int `json:"cols,omitempty"`
	Rows int `json:"rows,omitempty"`
}

// SnapshotResponse answers POST …/snapshot: the workspace saved to R2.
type SnapshotResponse struct {
	ID        string `json:"id"`
	Size      int64  `json:"size"`
	CreatedAt string `json:"createdAt"`
}
