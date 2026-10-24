package adkmemory

import (
	"time"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/session"
)

// turn is one thing said in a conversation, in the shape every backend reads
// before rendering it as its own: who said it, its text, and when.
type turn struct {
	// role is genai.RoleUser for the member and genai.RoleModel for
	// everything else.
	role  string
	texts []string
	at    time.Time
}

// spokenTurns is what was said in a conversation, as the turns a backend
// reads to work out what is worth remembering. Only text is kept: a tool's
// call and its result are how an answer was reached, not something the member
// told Krude, and they carry the contents of whatever the tools read. Neither
// is a model's thinking.
func spokenTurns(conversation session.Session) []turn {
	if conversation == nil {
		return nil
	}
	var said []turn
	for event := range conversation.Events().All() {
		if event == nil || event.Content == nil {
			continue
		}
		var texts []string
		for _, part := range event.Content.Parts {
			if part == nil || part.Text == "" || part.Thought {
				continue
			}
			texts = append(texts, part.Text)
		}
		if len(texts) == 0 {
			continue
		}
		role := event.Content.Role
		if role != genai.RoleUser {
			// Anything Krude, a sub-agent or a tool authored is the model's
			// side of the conversation.
			role = genai.RoleModel
		}
		said = append(said, turn{role: role, texts: texts, at: event.Timestamp})
	}
	return said
}

// memberSpoke reports whether any of turns is the member's. A run in which
// the member said nothing has nothing of theirs to remember.
func memberSpoke(turns []turn) bool {
	for _, said := range turns {
		if said.role == genai.RoleUser {
			return true
		}
	}
	return false
}
