package agents

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/genai"
)

// defaultArtifactMIMEType is what bytes of an unnamed kind are stored as.
const defaultArtifactMIMEType = "application/octet-stream"

type saveArtifactArgs struct {
	Filename      string `json:"filename" jsonschema:"Name to store it under, with its extension and no path separators. Saving the same name again adds a version rather than replacing it."`
	ContentBase64 string `json:"content_base64" jsonschema:"The bytes, base64 encoded — for sandbox output, exactly what sandbox_read_files returned."`
	MIMEType      string `json:"mime_type,omitempty" jsonschema:"The content's MIME type, such as text/csv or image/png. Defaults to application/octet-stream."`
}

type savedArtifact struct {
	ArtifactName    string `json:"artifact_name"`
	ArtifactVersion int64  `json:"artifact_version"`
	MIMEType        string `json:"mime_type"`
	SizeBytes       int    `json:"size_bytes"`
}

// saveArtifactTool keeps bytes the conversation produced as an ADK artifact,
// the same store browser_screenshot writes to and load_artifacts reads back.
// The sandbox is thrown away with the conversation; an artifact is not, and
// what it costs to keep one is the artifact service's decision rather than
// this tool's.
func saveArtifactTool() (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "save_artifact",
		Description: "Saves bytes you already have — a file the sandbox produced, something you generated — as an artifact of this conversation, so it outlives the sandbox that made it. Read one back with load_artifacts, using the name this returns.",
	}, func(ctx agent.Context, args saveArtifactArgs) (savedArtifact, error) {
		if ctx.Artifacts() == nil {
			return savedArtifact{}, errors.New("saving an artifact requires an ADK artifact service")
		}
		content, err := decodeBase64(args.ContentBase64)
		if err != nil {
			return savedArtifact{}, err
		}
		mimeType := strings.TrimSpace(args.MIMEType)
		if mimeType == "" {
			mimeType = defaultArtifactMIMEType
		}
		saved, err := ctx.Artifacts().Save(ctx, args.Filename, genai.NewPartFromBytes(content, mimeType))
		if err != nil {
			return savedArtifact{}, fmt.Errorf("save artifact: %w", err)
		}
		return savedArtifact{
			ArtifactName:    args.Filename,
			ArtifactVersion: saved.Version,
			MIMEType:        mimeType,
			SizeBytes:       len(content),
		}, nil
	})
}

// decodeBase64 reads standard base64, and unpadded base64 as well, because
// what reaches this tool has been through a model.
func decodeBase64(encoded string) ([]byte, error) {
	trimmed := strings.TrimSpace(encoded)
	if trimmed == "" {
		return nil, errors.New("content_base64 is empty")
	}
	content, err := base64.StdEncoding.DecodeString(trimmed)
	if err == nil {
		return content, nil
	}
	content, rawErr := base64.RawStdEncoding.DecodeString(trimmed)
	if rawErr != nil {
		return nil, fmt.Errorf("content_base64 is not base64: %w", err)
	}
	return content, nil
}
