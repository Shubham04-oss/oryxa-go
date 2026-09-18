package transform

import (
	"fmt"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

// HTML is read by taking its text out, not by storing its markup.
//
// Two reasons, and the second is the one that matters here. Markup is not
// what anyone searches for: nobody looks for a document by its class
// attributes. And the hash over a body is the document's version, so a
// provider that re-serializes the same page — reordering attributes, closing
// a tag it had left open, changing its indentation — must not look as though
// the document changed. Text survives all of that; markup does not.

// blockElements end a line. Without them a list renders as one long word:
// "<li>first</li><li>second</li>" would read "firstsecond", and a retrieval
// pipeline would index a term nobody will ever search for.
var blockElements = map[atom.Atom]bool{
	atom.Address: true, atom.Article: true, atom.Aside: true, atom.Blockquote: true,
	atom.Br: true, atom.Caption: true, atom.Dd: true, atom.Details: true, atom.Div: true,
	atom.Dl: true, atom.Dt: true, atom.Fieldset: true, atom.Figcaption: true, atom.Figure: true,
	atom.Footer: true, atom.Form: true, atom.H1: true, atom.H2: true, atom.H3: true,
	atom.H4: true, atom.H5: true, atom.H6: true, atom.Header: true, atom.Hr: true,
	atom.Li: true, atom.Main: true, atom.Nav: true, atom.Ol: true, atom.P: true,
	atom.Pre: true, atom.Section: true, atom.Summary: true, atom.Table: true,
	atom.Tbody: true, atom.Td: true, atom.Tfoot: true, atom.Th: true, atom.Thead: true,
	atom.Tr: true, atom.Ul: true,
}

// silentElements hold no prose. Script and style are code; their contents
// would otherwise be indexed as though someone had written them to be read.
var silentElements = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Noscript: true,
	atom.Template: true, atom.Svg: true, atom.Head: true,
}

// htmlText reads the words out of an HTML document.
//
// The title is kept, ahead of the body, because it is often the only place a
// page says what it is about — a provider that answers a whole page gives no
// other summary of it.
func htmlText(content []byte) (string, error) {
	document, err := html.Parse(strings.NewReader(string(content)))
	if err != nil {
		// x/net's parser recovers from almost anything, so this is close to
		// unreachable; a document it will not read is not one to index.
		return "", fmt.Errorf("this HTML could not be read: %w", domain.ErrUnsupportedType)
	}
	var text strings.Builder
	if title := htmlTitle(document); title != "" {
		text.WriteString(title)
		text.WriteString("\n\n")
	}
	collectText(document, &text)
	return text.String(), nil
}

// htmlTitle is the document's <title>, which lives in <head> and so is
// skipped by the walk that follows.
func htmlTitle(node *html.Node) string {
	if node.Type == html.ElementNode && node.DataAtom == atom.Title {
		var title strings.Builder
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if child.Type == html.TextNode {
				title.WriteString(child.Data)
			}
		}
		return strings.TrimSpace(title.String())
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if found := htmlTitle(child); found != "" {
			return found
		}
	}
	return ""
}

// collectText walks the tree, writing text and a newline wherever a block
// element begins or ends.
func collectText(node *html.Node, text *strings.Builder) {
	switch node.Type {
	case html.TextNode:
		text.WriteString(node.Data)
		return
	case html.CommentNode, html.DoctypeNode:
		// A comment is not what a page says, whatever it holds.
		return
	case html.ElementNode:
		if silentElements[node.DataAtom] {
			return
		}
		if blockElements[node.DataAtom] {
			text.WriteByte('\n')
		}
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		collectText(child, text)
	}
	if node.Type == html.ElementNode && blockElements[node.DataAtom] {
		text.WriteByte('\n')
	}
}
