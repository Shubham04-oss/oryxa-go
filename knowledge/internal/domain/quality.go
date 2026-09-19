package domain

// Candidate is what a judge sees of a document when deciding whether it is
// worth indexing: whether a member would ever want Krude to find it.
type Candidate struct {
	Provider string
	Kind     string
	Title    string
	// MIME is the type the document is kept as: the text's own for one
	// knowledge read, the provider's for one kept as its original bytes.
	MIME     string
	Metadata map[string]string
	// Text is the document's extracted text, for one knowledge reads itself.
	Text string
	// Original is the document's own bytes, for one kept as them (a PDF, an
	// Office file). A judge that reads the format looks inside; one that does
	// not judges it by the rest.
	Original []byte
	// Size is the document's size in bytes, text or original.
	Size int
}

// Verdict is what a judge decided about a Candidate.
type Verdict struct {
	Keep bool
	// Reason is a few words on why, which an excluded document keeps so a
	// member or an operator can see what was left out and on what grounds.
	Reason string
}
