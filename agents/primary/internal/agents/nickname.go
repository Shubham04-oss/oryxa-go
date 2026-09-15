package agents

import (
	"errors"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

type setNicknameArgs struct {
	Nickname string `json:"nickname" jsonschema:"The nickname the member gave you."`
}

type setNicknameResult struct {
	Nickname string `json:"nickname"`
}

// setNicknameTool saves the nickname the member gives Krude. It is user-scoped
// state, so the nickname follows the member into their other sessions.
func setNicknameTool() (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "set_nickname",
		Description: "Saves the nickname the member gives you, so you answer to it in this session and later ones.",
	}, func(ctx agent.Context, args setNicknameArgs) (setNicknameResult, error) {
		nickname := oneLine(args.Nickname)
		if nickname == "" {
			return setNicknameResult{}, errors.New("the nickname is empty")
		}
		if err := ctx.State().Set(NicknameKey, nickname); err != nil {
			return setNicknameResult{}, err
		}
		return setNicknameResult{Nickname: nickname}, nil
	})
}
