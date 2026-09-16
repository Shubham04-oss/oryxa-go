package agents

import (
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// userDetails is who Krude is assisting, as the session runtime seeded it from
// the member's auth record.
type userDetails struct {
	Name           string `json:"name"`
	About          string `json:"about,omitempty"`
	Email          string `json:"email,omitempty"`
	Role           string `json:"role,omitempty"`
	Organization   string `json:"organization"`
	OrganizationID string `json:"organization_id,omitempty"`
}

// userDetailsTool returns the member's details from session state, so Krude can
// check them again when a long conversation has moved them out of sight.
func userDetailsTool() (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "get_user_details",
		Description: "Returns who you are assisting from their Oryxa account: name, a few words about them, email, role and organization. Check them here instead of asking the member for what the account already knows.",
	}, func(ctx agent.Context, _ struct{}) (userDetails, error) {
		state := ctx.ReadonlyState()
		return userDetails{
			Name:           stateText(state, UserNameKey, ""),
			About:          stateText(state, UserAboutKey, ""),
			Email:          stateText(state, UserEmailKey, ""),
			Role:           stateText(state, UserRoleKey, ""),
			Organization:   stateText(state, OrgNameKey, ""),
			OrganizationID: stateText(state, OrganizationIDKey, ""),
		}, nil
	})
}
