// Package templates implements the ${type:name} string templating used
// by mcproxy config files.
package templates

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Reference identifies a templated value by its provider type and name.
// "${env:FOO}" parses to Reference{Type: "env", Name: "FOO"}.
type Reference struct {
	Type string
	Name string
}

// Resolver returns the value for a (type, name) reference.
type Resolver interface {
	Get(ctx context.Context, typ, name string) (string, error)
}

// TemplateString is a parsed templated string built from alternating
// literal segments and ${type:name} references.
type TemplateString struct {
	parts []part
	refs  []Reference
}

type part struct {
	literal string
	ref     Reference // zero when literal is set
}

// Parse parses s into a TemplateString.
func Parse(s string) (TemplateString, error) {
	var t TemplateString
	seen := map[Reference]struct{}{}
	rest := s
	for {
		i := strings.Index(rest, "${")
		if i < 0 {
			if rest != "" {
				t.parts = append(t.parts, part{literal: rest})
			}
			break
		}
		if i > 0 {
			t.parts = append(t.parts, part{literal: rest[:i]})
		}
		rest = rest[i+2:]
		end := strings.Index(rest, "}")
		if end < 0 {
			return TemplateString{}, fmt.Errorf("unterminated ${...} in %q", s)
		}
		expr := rest[:end]
		rest = rest[end+1:]
		typ, name, ok := strings.Cut(expr, ":")
		if !ok {
			return TemplateString{}, fmt.Errorf("template %q: expected ${type:name}", expr)
		}
		if typ == "" {
			return TemplateString{}, fmt.Errorf("template %q: empty type", expr)
		}
		if name == "" {
			return TemplateString{}, fmt.Errorf("template %q: empty name", expr)
		}
		ref := Reference{Type: typ, Name: name}
		t.parts = append(t.parts, part{ref: ref})
		if _, ok := seen[ref]; !ok {
			seen[ref] = struct{}{}
			t.refs = append(t.refs, ref)
		}
	}
	return t, nil
}

// References returns the deduped list of references in the template, in
// the order they first appear.
func (t TemplateString) References() []Reference {
	out := make([]Reference, len(t.refs))
	copy(out, t.refs)
	return out
}

// IsLiteral reports whether the template has no references.
func (t TemplateString) IsLiteral() bool {
	for _, p := range t.parts {
		if p.ref != (Reference{}) {
			return false
		}
	}
	return true
}

// Render renders the template by calling r.Get for each reference.
func (t TemplateString) Render(ctx context.Context, r Resolver) (string, error) {
	var sb strings.Builder
	for _, p := range t.parts {
		if p.ref == (Reference{}) {
			sb.WriteString(p.literal)
			continue
		}
		v, err := r.Get(ctx, p.ref.Type, p.ref.Name)
		if err != nil {
			return "", fmt.Errorf("resolve ${%s:%s}: %w", p.ref.Type, p.ref.Name, err)
		}
		sb.WriteString(v)
	}
	return sb.String(), nil
}

// UnmarshalJSON parses a JSON string into the template.
func (t *TemplateString) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	parsed, err := Parse(s)
	if err != nil {
		return err
	}
	*t = parsed
	return nil
}

// MarshalJSON returns the template's literal string. Errors if the
// template still contains unresolved references.
func (t TemplateString) MarshalJSON() ([]byte, error) {
	if !t.IsLiteral() {
		return nil, fmt.Errorf("templates: cannot marshal unrendered template")
	}
	var sb strings.Builder
	for _, p := range t.parts {
		sb.WriteString(p.literal)
	}
	return json.Marshal(sb.String())
}

// MergeReferences appends src's entries to dst, deduplicated.
func MergeReferences(dst, src []Reference) []Reference {
	for _, r := range src {
		found := false
		for _, e := range dst {
			if e == r {
				found = true
				break
			}
		}
		if !found {
			dst = append(dst, r)
		}
	}
	return dst
}
