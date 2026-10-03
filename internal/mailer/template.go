package mailer

import (
	"fmt"
	htmltemplate "html/template"
	"strings"
	texttemplate "text/template"
)

// Template holds the parsed subject/text/html templates of a bulk send.
// Templates use Go template syntax, e.g. "Halo {{.name}}". Every referenced
// key must exist in the recipient data; a missing key is an error rather
// than silently rendering "<no value>".
type Template struct {
	subject *texttemplate.Template
	text    *texttemplate.Template
	html    *htmltemplate.Template
}

// ParseTemplate parses the three message parts. Empty parts stay empty.
func ParseTemplate(subject, text, html string) (*Template, error) {
	t := &Template{}
	var err error
	if t.subject, err = texttemplate.New("subject").Option("missingkey=error").Parse(subject); err != nil {
		return nil, fmt.Errorf("subject template: %w", err)
	}
	if text != "" {
		if t.text, err = texttemplate.New("text").Option("missingkey=error").Parse(text); err != nil {
			return nil, fmt.Errorf("text template: %w", err)
		}
	}
	if html != "" {
		// html/template escapes recipient data to prevent HTML injection.
		if t.html, err = htmltemplate.New("html").Option("missingkey=error").Parse(html); err != nil {
			return nil, fmt.Errorf("html template: %w", err)
		}
	}
	return t, nil
}

// Rendered is the per-recipient output of a Template.
type Rendered struct {
	Subject, Text, HTML string
}

// Render executes the templates with data.
func (t *Template) Render(data map[string]any) (Rendered, error) {
	var r Rendered
	var b strings.Builder
	if err := t.subject.Execute(&b, data); err != nil {
		return r, fmt.Errorf("subject template: %w", err)
	}
	r.Subject = b.String()
	if t.text != nil {
		b.Reset()
		if err := t.text.Execute(&b, data); err != nil {
			return r, fmt.Errorf("text template: %w", err)
		}
		r.Text = b.String()
	}
	if t.html != nil {
		b.Reset()
		if err := t.html.Execute(&b, data); err != nil {
			return r, fmt.Errorf("html template: %w", err)
		}
		r.HTML = b.String()
	}
	return r, nil
}

// TemplateData merges a recipient's address into its custom data so
// templates can always use {{.email}} and {{.name}}.
func TemplateData(a Address, data map[string]any) map[string]any {
	out := make(map[string]any, len(data)+2)
	out["email"] = a.Email
	out["name"] = a.Name
	for k, v := range data {
		out[k] = v
	}
	return out
}
