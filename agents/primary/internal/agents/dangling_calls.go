package agents

import (
	"google.golang.org/genai"
)

// interruptedResult is the result given to a function call that nobody
// answered. Whether its tool ran, and what it did, is not known.
const interruptedResult = "This call was interrupted before it returned, so there is no result. It may or may not have taken effect; check before repeating it."

// answerDanglingCalls returns contents with a function response for every
// function call that has none.
//
// A run cancelled after a model turn was stored and before its tools
// answered, as a deploy cancels one, leaves the thread's history ending in
// calls nobody answered, and ADK sends it as it is. Vertex refuses a
// function call turn unless the turn after it holds a response for each
// call, so it would refuse every later request on the thread the same way.
// The missing responses say the call was interrupted. They go in a turn of
// their own after the calls when the turn after has no responses at all, and
// among its responses when it has some.
//
// A response answers the call with its id where there is one, otherwise the
// first call of its name that has no response yet. What it changes it
// copies, since a request's contents can be the session's own events.
func answerDanglingCalls(contents []*genai.Content) []*genai.Content {
	var added, completed map[int]*genai.Content
	for index, content := range contents {
		if content == nil {
			continue
		}
		calls := callParts(content.Parts)
		if len(calls) == 0 {
			continue
		}
		var following *genai.Content
		if index+1 < len(contents) {
			following = contents[index+1]
		}
		var followingParts []*genai.Part
		if following != nil {
			followingParts = following.Parts
		}
		responses := responseParts(followingParts)
		// Vertex counts the responses against the calls, so a turn with as
		// many as there are calls is left alone, whatever they are for.
		if len(responses) >= len(calls) {
			continue
		}
		if len(responses) == 0 {
			if added == nil {
				added = map[int]*genai.Content{}
			}
			added[index] = &genai.Content{Role: genai.RoleUser, Parts: answerAll(calls, nil)}
			continue
		}
		if completed == nil {
			completed = map[int]*genai.Content{}
		}
		completed[index+1] = &genai.Content{Role: following.Role, Parts: answerAll(calls, followingParts)}
	}
	if len(added) == 0 && len(completed) == 0 {
		return contents
	}
	repaired := make([]*genai.Content, 0, len(contents)+len(added))
	for index, content := range contents {
		if completion, ok := completed[index]; ok {
			content = completion
		}
		repaired = append(repaired, content)
		if turn, ok := added[index]; ok {
			repaired = append(repaired, turn)
		}
	}
	return repaired
}

// answerAll returns a function response for each of calls, in the order of
// the calls: the one among parts that answers it, or an interrupted one when
// there is none. The rest of parts follows.
func answerAll(calls, parts []*genai.Part) []*genai.Part {
	responses := responseParts(parts)
	answers := make([]*genai.Part, len(calls))
	taken := map[*genai.Part]bool{}
	// By id where a call has one, then by name in order. A call and a response
	// that both have an id, and not the same one, are for different calls.
	for _, byID := range []bool{true, false} {
		for i, call := range calls {
			if answers[i] != nil || (byID && call.FunctionCall.ID == "") {
				continue
			}
			for _, response := range responses {
				if taken[response] {
					continue
				}
				if byID && response.FunctionResponse.ID != call.FunctionCall.ID {
					continue
				}
				if !byID && (response.FunctionResponse.Name != call.FunctionCall.Name ||
					(call.FunctionCall.ID != "" && response.FunctionResponse.ID != "")) {
					continue
				}
				answers[i], taken[response] = response, true
				break
			}
		}
	}
	all := make([]*genai.Part, 0, len(calls)+len(parts))
	for i, call := range calls {
		if answers[i] == nil {
			answers[i] = &genai.Part{FunctionResponse: &genai.FunctionResponse{
				ID:       call.FunctionCall.ID,
				Name:     call.FunctionCall.Name,
				Response: map[string]any{"error": interruptedResult},
			}}
		}
		all = append(all, answers[i])
	}
	for _, part := range parts {
		if part != nil && !taken[part] {
			all = append(all, part)
		}
	}
	return all
}

// callParts returns the parts that are function calls.
func callParts(parts []*genai.Part) []*genai.Part {
	var calls []*genai.Part
	for _, part := range parts {
		if part != nil && part.FunctionCall != nil {
			calls = append(calls, part)
		}
	}
	return calls
}

// responseParts returns the parts that are function responses.
func responseParts(parts []*genai.Part) []*genai.Part {
	var responses []*genai.Part
	for _, part := range parts {
		if part != nil && part.FunctionResponse != nil {
			responses = append(responses, part)
		}
	}
	return responses
}
