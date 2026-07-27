// Package schemaform parses the SMALL JSON-Schema subset Gridwell uses for
// creation parameters (issue #198) and validates values against it. This is
// deliberately a form-description language, not a JSON Schema validator:
// string and number types, enum, required, title, and a "secret" format
// hint that masks INPUT only (the policy that secret VALUES never ride
// parameters — a field names a path the plugin resolves on its own host —
// is the plugin's contract; the hint just keeps shoulder-surfing honest).
//
// The client renders a form from Parse and pre-validates with Validate as
// pure UX; the PLUGIN validates authoritatively at commit and its refusal
// surfaces on the error strip. This package can never become an authority —
// rejection is always the plugin's (charter §1: the schema has one owner).
package schemaform

import (
	"encoding/json"
	"fmt"
	"sort"
)

// Field is one form input, in declaration order.
type Field struct {
	Name     string
	Title    string // display label; falls back to Name
	Type     string // "string" | "number"
	Enum     []string
	Required bool
	Secret   bool // mask the input widget; storage is unaffected
}

// Form is a parsed creation schema for one tile kind.
type Form struct {
	Fields []Field
}

// jsonSchema is the accepted wire shape.
type jsonSchema struct {
	Type       string               `json:"type"`
	Properties map[string]jsonField `json:"properties"`
	Required   []string             `json:"required"`
}

type jsonField struct {
	Type   string   `json:"type"`
	Title  string   `json:"title"`
	Enum   []string `json:"enum"`
	Format string   `json:"format"`
}

// Parse decodes a creation schema. Unknown top-level or field keys are
// ignored (a newer plugin may declare more than this client understands —
// the plugin still validates authoritatively); unsupported field TYPES are
// an error, because rendering a field wrong is worse than refusing the form.
func Parse(schema string) (*Form, error) {
	var s jsonSchema
	if err := json.Unmarshal([]byte(schema), &s); err != nil {
		return nil, fmt.Errorf("schemaform: %w", err)
	}
	if s.Type != "object" {
		return nil, fmt.Errorf("schemaform: top-level type must be \"object\", got %q", s.Type)
	}
	req := map[string]bool{}
	for _, r := range s.Required {
		req[r] = true
	}
	f := &Form{}
	// Deterministic field order: JSON maps are unordered, so sort by name —
	// a stable form beats a shuffling one. (Declaration order would need an
	// ordered decoder; revisit if a plugin ever needs it.)
	names := make([]string, 0, len(s.Properties))
	for name := range s.Properties {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p := s.Properties[name]
		switch p.Type {
		case "string", "number":
		default:
			return nil, fmt.Errorf("schemaform: field %q: unsupported type %q", name, p.Type)
		}
		title := p.Title
		if title == "" {
			title = name
		}
		f.Fields = append(f.Fields, Field{
			Name:     name,
			Title:    title,
			Type:     p.Type,
			Enum:     p.Enum,
			Required: req[name],
			Secret:   p.Format == "secret",
		})
	}
	return f, nil
}

// Validate checks values against the form: required fields present and
// non-empty, numbers numeric, enums within their set. Returns one message
// per problem, empty = valid. Pure pre-validation — the plugin's commit-time
// verdict is the authority.
func (f *Form) Validate(values map[string]string) []string {
	var errs []string
	for _, fd := range f.Fields {
		v, ok := values[fd.Name]
		if !ok || v == "" {
			if fd.Required {
				errs = append(errs, fmt.Sprintf("%s is required", fd.Title))
			}
			continue
		}
		if fd.Type == "number" {
			var n float64
			if err := json.Unmarshal([]byte(v), &n); err != nil {
				errs = append(errs, fmt.Sprintf("%s must be a number", fd.Title))
				continue
			}
		}
		if len(fd.Enum) > 0 {
			found := false
			for _, e := range fd.Enum {
				found = found || e == v
			}
			if !found {
				errs = append(errs, fmt.Sprintf("%s must be one of %v", fd.Title, fd.Enum))
			}
		}
	}
	return errs
}

// Encode builds the params document the created tile's WriteContent carries:
// a flat JSON object, numbers as numbers, everything else as strings. Only
// non-empty values are included (absent optional = absent key).
func (f *Form) Encode(values map[string]string) ([]byte, error) {
	if errs := f.Validate(values); len(errs) > 0 {
		return nil, fmt.Errorf("schemaform: %v", errs)
	}
	out := map[string]any{}
	for _, fd := range f.Fields {
		v, ok := values[fd.Name]
		if !ok || v == "" {
			continue
		}
		if fd.Type == "number" {
			var n float64
			_ = json.Unmarshal([]byte(v), &n)
			out[fd.Name] = n
		} else {
			out[fd.Name] = v
		}
	}
	return json.Marshal(out)
}
