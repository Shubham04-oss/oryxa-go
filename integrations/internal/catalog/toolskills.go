package catalog

import (
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/actionflow"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/oauthflow"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/toolskills"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/triggerflow"
)

type ActionTool struct {
	Definition actionflow.Definition
	Skill      toolskills.Skill
}

type TriggerTool struct {
	Definition triggerflow.Definition
	Skill      toolskills.Skill
}

type ConnectionTool struct {
	Definition oauthflow.Definition
	Skill      toolskills.Skill
}

// ActionTool returns an execution definition together with the instructions a
// direct tool adapter injects, or an MCP server links as a resource. Returning
// one binding object prevents adapters from exposing a tool without its skill.
func (c Static) ActionTool(provider, actionName string) (ActionTool, bool) {
	definition, ok := c.ActionDefinition(provider, actionName)
	if !ok {
		return ActionTool{}, false
	}
	skill, ok := toolskills.ForAction(provider, actionName)
	return ActionTool{Definition: definition, Skill: skill}, ok
}

func (c Static) TriggerTool(provider, triggerName string) (TriggerTool, bool) {
	definition, ok := c.TriggerDefinition(provider, triggerName)
	if !ok {
		return TriggerTool{}, false
	}
	skill, ok := toolskills.ForTrigger(provider, triggerName)
	return TriggerTool{Definition: definition, Skill: skill}, ok
}

func (c Static) ConnectionTool(provider string) (ConnectionTool, bool) {
	definition, ok := c.OAuthDefinition(provider)
	if !ok {
		return ConnectionTool{}, false
	}
	skill, ok := toolskills.ForConnection(provider)
	return ConnectionTool{Definition: definition, Skill: skill}, ok
}

func bindActionSkill(provider, actionName string, definition actionflow.Definition, ok bool) (actionflow.Definition, bool) {
	if !ok {
		return actionflow.Definition{}, false
	}
	if _, bound := toolskills.ForAction(provider, actionName); !bound {
		return actionflow.Definition{}, false
	}
	return definition, true
}

func bindTriggerSkill(provider, triggerName string, definition triggerflow.Definition, ok bool) (triggerflow.Definition, bool) {
	if !ok {
		return triggerflow.Definition{}, false
	}
	if _, bound := toolskills.ForTrigger(provider, triggerName); !bound {
		return triggerflow.Definition{}, false
	}
	return definition, true
}

func bindConnectionSkill(provider string, definition oauthflow.Definition, ok bool) (oauthflow.Definition, bool) {
	if !ok {
		return oauthflow.Definition{}, false
	}
	if _, bound := toolskills.ForConnection(provider); !bound {
		return oauthflow.Definition{}, false
	}
	return definition, true
}
