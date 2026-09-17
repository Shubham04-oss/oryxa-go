package chunking

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html"
	"slices"
	"strings"
)

// Normalize turns a supported content type into the markdown-shaped text that
// is stored, chunked and cited.
//
// It runs before chunking and its output is what the document row keeps, so
// every chunk offset points at text that still exists. Normalizing at read
// time instead would leave citations addressing a string that was thrown away.
//
// Anything it cannot improve on it returns with line endings tidied, which is
// the one change every type gets: a stray carriage return otherwise ends up
// inside chunk text and inside the embedded string.
func Normalize(text, contentType string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	switch contentType {
	case "text/html":
		return htmlToText(text)
	case "text/csv":
		return csvToText(text)
	case "application/json":
		return jsonToText(text)
	default:
		return text
	}
}

// blockTags are the elements that end a line of text. Anything not listed is
// inline and its text runs on, which is what keeps a sentence with a link in
// the middle from being cut into three.
var blockTags = []string{
	"address", "article", "aside", "blockquote", "br", "div", "dd", "dl", "dt",
	"fieldset", "figcaption", "figure", "footer", "form", "header", "hr", "li",
	"main", "nav", "ol", "p", "pre", "section", "table", "tbody", "td", "tfoot",
	"th", "thead", "tr", "ul",
}

// droppedTags are elements whose text is never prose. Indexing a stylesheet or
// a script body produces chunks that match nothing and cost money to embed.
var droppedTags = []string{"script", "style", "noscript", "svg", "template"}

// htmlToText reduces HTML to markdown-shaped text, keeping headings, list
// items and block boundaries and dropping everything else.
//
// This is a reduction, not a parser. Rag does not own extraction: a real page
// with client-side rendering belongs to the upstream pipeline, which can
// deliver text or markdown directly. What this handles is the ordinary case of
// stored HTML that would otherwise be indexed with its tags.
func htmlToText(source string) string {
	var out strings.Builder
	out.Grow(len(source))
	var skipUntil string
	for position := 0; position < len(source); {
		next := strings.IndexByte(source[position:], '<')
		if next < 0 {
			if skipUntil == "" {
				out.WriteString(html.UnescapeString(source[position:]))
			}
			break
		}
		if skipUntil == "" {
			out.WriteString(html.UnescapeString(source[position : position+next]))
		}
		position += next
		closeAt := strings.IndexByte(source[position:], '>')
		if closeAt < 0 {
			break
		}
		raw := source[position+1 : position+closeAt]
		position += closeAt + 1

		name, closing := tagName(raw)
		if skipUntil != "" {
			if closing && name == skipUntil {
				skipUntil = ""
			}
			continue
		}
		switch {
		case !closing && slices.Contains(droppedTags, name):
			// A self-closing dropped tag has nothing to skip to.
			if !strings.HasSuffix(raw, "/") {
				skipUntil = name
			}
		case len(name) == 2 && name[0] == 'h' && name[1] >= '1' && name[1] <= '6':
			if closing {
				out.WriteString("\n\n")
			} else {
				out.WriteString("\n\n" + strings.Repeat("#", int(name[1]-'0')) + " ")
			}
		case name == "li" && !closing:
			out.WriteString("\n- ")
		case slices.Contains(blockTags, name):
			out.WriteString("\n\n")
		}
	}
	return collapseBlankLines(out.String())
}

// tagName reads an element name out of the inside of a tag, and says whether
// it was a closing tag.
func tagName(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	closing := strings.HasPrefix(raw, "/")
	raw = strings.TrimPrefix(raw, "/")
	end := strings.IndexAny(raw, " \t\n/>")
	if end >= 0 {
		raw = raw[:end]
	}
	return strings.ToLower(raw), closing
}

// collapseBlankLines squeezes runs of blank lines down to one, so the block
// boundaries the segmenter looks for stay meaningful.
func collapseBlankLines(text string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		if strings.TrimSpace(line) == "" {
			blank++
			if blank > 1 {
				continue
			}
			out = append(out, "")
			continue
		}
		blank = 0
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n")) + "\n"
}

// maxTabularRecords bounds how many rows one document contributes, so a
// mis-delivered export cannot turn into a million embedding calls.
const maxTabularRecords = 20000

// csvToText turns rows into labelled blocks.
//
// A bare CSV row embeds terribly: "acme,42,2024-01-01" shares no words with
// any question anyone would ask. Pairing each value with its column name
// gives the row the vocabulary it was missing, and a blank line between rows
// lets the packer fit as many whole rows into a chunk as the budget allows.
func csvToText(source string) string {
	reader := csv.NewReader(strings.NewReader(source))
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true
	reader.ReuseRecord = true

	header, err := reader.Read()
	if err != nil {
		return source
	}
	columns := slices.Clone(header)

	var out strings.Builder
	out.Grow(len(source))
	for count := 0; count < maxTabularRecords; count++ {
		record, err := reader.Read()
		if err != nil {
			break
		}
		block := make([]string, 0, len(record))
		for index, value := range record {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			column := fmt.Sprintf("column_%d", index+1)
			if index < len(columns) && strings.TrimSpace(columns[index]) != "" {
				column = strings.TrimSpace(columns[index])
			}
			block = append(block, column+": "+value)
		}
		if len(block) == 0 {
			continue
		}
		out.WriteString(strings.Join(block, "\n"))
		out.WriteString("\n\n")
	}
	if out.Len() == 0 {
		return source
	}
	return collapseBlankLines(out.String())
}

// jsonToText flattens JSON into labelled lines, one block per record.
//
// An array of objects becomes one block per element, which is what a log
// export or an API dump looks like. Anything else becomes a single flattened
// block. Indexing raw JSON instead would spend most of every chunk on braces
// and quotation marks.
func jsonToText(source string) string {
	var decoded any
	if err := json.Unmarshal([]byte(source), &decoded); err != nil {
		return source
	}
	var blocks []string
	if records, ok := decoded.([]any); ok {
		for index, record := range records {
			if index >= maxTabularRecords {
				break
			}
			if block := flatten(record, ""); block != "" {
				blocks = append(blocks, block)
			}
		}
	} else if block := flatten(decoded, ""); block != "" {
		blocks = append(blocks, block)
	}
	if len(blocks) == 0 {
		return source
	}
	return collapseBlankLines(strings.Join(blocks, "\n\n"))
}

// maxFlattenDepth stops a deeply nested or self-referential structure from
// producing an unbounded number of paths.
const maxFlattenDepth = 8

func flatten(value any, prefix string) string {
	return strings.Join(flattenInto(nil, value, prefix, 0), "\n")
}

func flattenInto(lines []string, value any, prefix string, depth int) []string {
	if depth > maxFlattenDepth {
		return lines
	}
	switch typed := value.(type) {
	case map[string]any:
		// Sorted, because the same document must produce the same chunks: map
		// order would make every re-ingest look like a change and re-embed
		// the whole corpus.
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			lines = flattenInto(lines, typed[key], join(prefix, key), depth+1)
		}
	case []any:
		for index, item := range typed {
			lines = flattenInto(lines, item, fmt.Sprintf("%s[%d]", prefix, index), depth+1)
		}
	case nil:
		// A null carries no meaning worth a token.
	default:
		text := strings.TrimSpace(fmt.Sprintf("%v", typed))
		if text != "" {
			label := prefix
			if label == "" {
				label = "value"
			}
			lines = append(lines, label+": "+text)
		}
	}
	return lines
}

func join(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}
