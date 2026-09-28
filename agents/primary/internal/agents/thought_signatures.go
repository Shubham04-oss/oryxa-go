package agents

import (
	"context"
	"iter"
	"reflect"
	"slices"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/model"
)

// foldedSignatures sends every request with its history's thought signatures
// on parts Vertex accepts.
//
// Streamed, Gemini 3 ends an answer with a chunk whose only part is an empty
// text carrying the answer's thought signature. ADK's stream aggregator keeps
// that part as a part of its own, the session stores it, and every later
// request sends it back as {"thoughtSignature": ...}: a part with no text,
// call or data. Vertex refuses such a request some of the time with 400
// INVALID_ARGUMENT, "Request contains an invalid argument.", and passes the
// same request other times. A signature on the part before it — the shape an
// unstreamed answer has — is always accepted.
type foldedSignatures struct {
	model.LLM
}

func (m foldedSignatures) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	req.Contents = foldSignatures(req.Contents)
	return m.LLM.GenerateContent(ctx, req, stream)
}

// foldSignatures returns contents with every signature-only part folded into
// the part before it. What it changes it copies, since a request's contents
// can be the session's own events.
func foldSignatures(contents []*genai.Content) []*genai.Content {
	var folded []*genai.Content
	for index, content := range contents {
		if content == nil || !slices.ContainsFunc(content.Parts, signatureOnly) {
			if folded != nil {
				folded = append(folded, content)
			}
			continue
		}
		if folded == nil {
			folded = append(make([]*genai.Content, 0, len(contents)), contents[:index]...)
		}
		parts := make([]*genai.Part, 0, len(content.Parts))
		for _, part := range content.Parts {
			if !signatureOnly(part) {
				parts = append(parts, part)
				continue
			}
			// With nothing unsigned before it to carry it, the signature is
			// dropped: Gemini validates the signatures of function calls, and
			// a function call part keeps its own.
			if last := len(parts) - 1; last >= 0 && len(parts[last].ThoughtSignature) == 0 {
				signed := *parts[last]
				signed.ThoughtSignature = part.ThoughtSignature
				parts[last] = &signed
			}
		}
		if len(parts) > 0 {
			folded = append(folded, &genai.Content{Role: content.Role, Parts: parts})
		}
	}
	if folded == nil {
		return contents
	}
	return folded
}

// signatureOnly reports a part that is empty but for its thought signature,
// the part ADK's own filter for Gemini 3's empty last chunk lets through.
func signatureOnly(part *genai.Part) bool {
	if part == nil || len(part.ThoughtSignature) == 0 {
		return false
	}
	rest := *part
	rest.ThoughtSignature, rest.Thought = nil, false
	return reflect.ValueOf(rest).IsZero()
}
