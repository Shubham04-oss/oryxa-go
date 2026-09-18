package transform

import (
	"strings"
	"testing"
)

// TestHTMLIsReadAsTheWordsSomebodyWrote covers what indexing HTML is for:
// the prose, without the markup nobody searches for.
func TestHTMLIsReadAsTheWordsSomebodyWrote(t *testing.T) {
	t.Parallel()
	for name, testCase := range map[string]struct {
		html string
		// holds are phrases the text must carry.
		holds []string
		// lacks are what must not survive.
		lacks []string
	}{
		"prose and its title": {
			html:  `<html><head><title>The handbook</title></head><body><p>How we work.</p></body></html>`,
			holds: []string{"The handbook", "How we work."},
			lacks: []string{"<p>", "html"},
		},
		"script and style are not prose": {
			html:  `<body><script>var secret = "token";</script><style>.a{color:red}</style><p>Readable.</p></body>`,
			holds: []string{"Readable."},
			lacks: []string{"secret", "token", "color:red"},
		},
		"fallback markup no browser shows is not prose": {
			html:  `<body><iframe><p>frame fallback</p></iframe><noembed>plugin fallback</noembed><p>Shown.</p></body>`,
			holds: []string{"Shown."},
			lacks: []string{"fallback", "<p>"},
		},
		"a comment is not what the page says": {
			html:  `<body><!-- an internal note --><p>Published.</p></body>`,
			holds: []string{"Published."},
			lacks: []string{"internal note"},
		},
		"attributes are not indexed": {
			html:  `<body><div class="sidebar" data-id="42"><p>Content.</p></div></body>`,
			holds: []string{"Content."},
			lacks: []string{"sidebar", "data-id", "42"},
		},
		"an entity becomes the character it names": {
			html:  `<body><p>Ben &amp; Jerry&rsquo;s &lt;tag&gt;</p></body>`,
			holds: []string{"Ben & Jerry", "<tag>"},
		},
		"unicode survives": {
			html:  `<body><p>Ünïcode 中文 🎉</p></body>`,
			holds: []string{"Ünïcode", "中文", "🎉"},
		},
		"markup a browser would have to repair": {
			html:  `<body><p>First<p>Second<ul><li>One<li>Two</body>`,
			holds: []string{"First", "Second", "One", "Two"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			text, err := Normalize([]byte(testCase.html), "text/html")
			if err != nil {
				t.Fatalf("Normalize() error = %v", err)
			}
			for _, want := range testCase.holds {
				if !strings.Contains(text.Body, want) {
					t.Fatalf("the text does not carry %q:\n%s", want, text.Body)
				}
			}
			for _, unwanted := range testCase.lacks {
				if strings.Contains(text.Body, unwanted) {
					t.Fatalf("the text still carries %q:\n%s", unwanted, text.Body)
				}
			}
		})
	}
}

// TestBlockElementsKeepWordsApart is the one that decides whether indexing
// HTML is worth doing at all: a list that runs together indexes terms nobody
// will ever search for.
func TestBlockElementsKeepWordsApart(t *testing.T) {
	t.Parallel()
	for name, markup := range map[string]string{
		"list items":   `<body><ul><li>alpha</li><li>beta</li></ul></body>`,
		"paragraphs":   `<body><p>alpha</p><p>beta</p></body>`,
		"table cells":  `<body><table><tr><td>alpha</td><td>beta</td></tr></table></body>`,
		"headings":     `<body><h1>alpha</h1><h2>beta</h2></body>`,
		"line breaks":  `<body>alpha<br>beta</body>`,
		"nested divs":  `<body><div>alpha</div><div>beta</div></body>`,
		"a definition": `<body><dl><dt>alpha</dt><dd>beta</dd></dl></body>`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			text, err := Normalize([]byte(markup), "text/html")
			if err != nil {
				t.Fatalf("Normalize() error = %v", err)
			}
			if strings.Contains(text.Body, "alphabeta") {
				t.Fatalf("two words ran together:\n%s", text.Body)
			}
			if !strings.Contains(text.Body, "alpha") || !strings.Contains(text.Body, "beta") {
				t.Fatalf("a word was lost:\n%s", text.Body)
			}
		})
	}
}

// TestInlineElementsDoNotBreakAWord is the other half: emphasis inside a
// sentence must not split it.
func TestInlineElementsDoNotBreakAWord(t *testing.T) {
	t.Parallel()
	text, err := Normalize([]byte(`<body><p>un<em>break</em>able</p></body>`), "text/html")
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if !strings.Contains(text.Body, "unbreakable") {
		t.Fatalf("an inline element split a word:\n%s", text.Body)
	}
}

// TestHTMLIsStoredAsTheTextItBecame keeps the stored media type honest: the
// body is no longer markup, so it must not say it is.
func TestHTMLIsStoredAsTheTextItBecame(t *testing.T) {
	t.Parallel()
	text, err := Normalize([]byte(`<body><p>Words.</p></body>`), "text/html; charset=utf-8")
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if text.MIME != MIMEPlain {
		t.Fatalf("MIME = %q, want the plain text the body now is", text.MIME)
	}
}

// TestTheHashIsOverTheWordsNotTheMarkup is why the text is taken out before
// hashing: a provider re-serializing the same page has not changed it, and
// an unchanged hash is how a pass over thousands of documents publishes
// nothing.
func TestTheHashIsOverTheWordsNotTheMarkup(t *testing.T) {
	t.Parallel()
	first, err := Normalize([]byte(`<body><p class="a">Same words.</p></body>`), "text/html")
	if err != nil {
		t.Fatal(err)
	}
	second, err := Normalize([]byte("<body>\n  <p class=\"a\" id=\"x\">Same words.</p>\n</body>"), "text/html")
	if err != nil {
		t.Fatal(err)
	}
	if first.Hash != second.Hash {
		t.Fatalf("the same words hashed differently: %s and %s\n%q\n%q",
			first.Hash, second.Hash, first.Body, second.Body)
	}
}

func TestXHTMLIsReadAsHTML(t *testing.T) {
	t.Parallel()
	text, err := Normalize([]byte(`<html><body><p>Words.</p></body></html>`), "application/xhtml+xml")
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if !strings.Contains(text.Body, "Words.") {
		t.Fatalf("body = %q", text.Body)
	}
}

func TestAnHTMLFilenameIsRecognized(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"page.html", "page.htm", "page.xhtml", "PAGE.HTML"} {
		if got := MIMEForFilename(name); got != MIMEHTML {
			t.Fatalf("MIMEForFilename(%q) = %q, want %q", name, got, MIMEHTML)
		}
	}
}
