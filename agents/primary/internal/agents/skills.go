package agents

import (
	"context"
	"embed"
	"io/fs"

	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/skilltoolset"
	"google.golang.org/adk/v2/tool/skilltoolset/skill"
)

// skillFiles holds Krude's skills: one folder per skill under skills/, each with
// a SKILL.md whose frontmatter names and describes it, and optional
// references/ and assets/ folders.
//
//go:embed skills
var skillFiles embed.FS

// newSkills returns ADK's skill toolset over the embedded skills. Every request
// lists each skill's name and description; the agent loads a skill's full
// instructions with load_skill only when the skill fits the work.
func newSkills(ctx context.Context) (tool.Toolset, error) {
	root, err := fs.Sub(skillFiles, "skills")
	if err != nil {
		return nil, err
	}
	source, _, err := skill.WithCompletePreloadSource(ctx, skill.NewFileSystemSource(root))
	if err != nil {
		return nil, err
	}
	return skilltoolset.New(ctx, skilltoolset.Config{Source: source})
}
