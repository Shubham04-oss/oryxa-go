package mail

import (
	"bytes"
	"embed"
	"fmt"
	htmltemplate "html/template"
	"io/fs"
	"strings"
	texttemplate "text/template"
)

//go:embed templates
var templateFiles embed.FS

// Message is a rendered email, ready to address and send.
type Message struct {
	Template string
	Subject  string
	HTML     string
	Text     string
}

// Templates holds every email under templates/: one directory per email with a
// subject.txt, a body.txt, and a body.html that fills layout.html's "content"
// and can use its "action" button.
type Templates struct {
	byName map[string]templateSet
}

type templateSet struct {
	subject *texttemplate.Template
	text    *texttemplate.Template
	html    *htmltemplate.Template
}

func ParseTemplates() (*Templates, error) {
	layout, err := htmltemplate.ParseFS(templateFiles, "templates/layout.html")
	if err != nil {
		return nil, fmt.Errorf("parse email layout: %w", err)
	}
	entries, err := fs.ReadDir(templateFiles, "templates")
	if err != nil {
		return nil, fmt.Errorf("read email templates: %w", err)
	}

	templates := &Templates{byName: map[string]templateSet{}}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		directory := "templates/" + name

		subject, err := texttemplate.ParseFS(templateFiles, directory+"/subject.txt")
		if err != nil {
			return nil, fmt.Errorf("parse %s subject: %w", name, err)
		}
		text, err := texttemplate.ParseFS(templateFiles, directory+"/body.txt")
		if err != nil {
			return nil, fmt.Errorf("parse %s text body: %w", name, err)
		}
		html, err := htmltemplate.Must(layout.Clone()).ParseFS(templateFiles, directory+"/body.html")
		if err != nil {
			return nil, fmt.Errorf("parse %s HTML body: %w", name, err)
		}
		templates.byName[name] = templateSet{subject: subject, text: text, html: html}
	}
	return templates, nil
}

func (t *Templates) Render(name string, data any) (Message, error) {
	set, ok := t.byName[name]
	if !ok {
		return Message{}, fmt.Errorf("unknown email template %q", name)
	}

	var subject, text, html bytes.Buffer
	if err := set.subject.Execute(&subject, data); err != nil {
		return Message{}, fmt.Errorf("render %s subject: %w", name, err)
	}
	if err := set.text.Execute(&text, data); err != nil {
		return Message{}, fmt.Errorf("render %s text body: %w", name, err)
	}
	if err := set.html.ExecuteTemplate(&html, "layout.html", data); err != nil {
		return Message{}, fmt.Errorf("render %s HTML body: %w", name, err)
	}

	return Message{
		Template: name,
		// Collapsing whitespace keeps a newline in the data from reaching the
		// Subject header.
		Subject: strings.Join(strings.Fields(subject.String()), " "),
		HTML:    html.String(),
		Text:    strings.TrimSpace(text.String()) + "\n",
	}, nil
}
