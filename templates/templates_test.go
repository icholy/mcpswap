package templates_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/icholy/mcproxy/templates"
)

// staticResolver is a Resolver backed by a fixed map.
type staticResolver map[string]string

func (s staticResolver) Get(_ context.Context, typ, name string) (string, error) {
	v, ok := s[typ+":"+name]
	if !ok {
		return "", fmt.Errorf("missing %s:%s", typ, name)
	}
	return v, nil
}

func TestParse_Literal(t *testing.T) {
	tpl, err := templates.Parse("hello world")
	assert.NilError(t, err)
	assert.Assert(t, tpl.IsLiteral())
	assert.Equal(t, len(tpl.References()), 0)
}

func TestParse_References(t *testing.T) {
	tpl, err := templates.Parse("Bearer ${env:TOKEN} for ${env:USER}/${env:TOKEN}")
	assert.NilError(t, err)
	assert.Assert(t, !tpl.IsLiteral())
	refs := tpl.References()
	assert.DeepEqual(t, refs, []templates.Reference{
		{Type: "env", Name: "TOKEN"},
		{Type: "env", Name: "USER"},
	})
}

func TestParse_Errors(t *testing.T) {
	cases := []string{
		"unterminated ${env:FOO",
		"empty ${:NAME}",
		"empty ${env:}",
		"no colon ${env}",
	}
	for _, in := range cases {
		_, err := templates.Parse(in)
		assert.Assert(t, err != nil, "expected error for %q", in)
	}
}

func TestRender(t *testing.T) {
	tpl, err := templates.Parse("Bearer ${env:TOKEN}")
	assert.NilError(t, err)
	got, err := tpl.Render(context.Background(), staticResolver{"env:TOKEN": "abc123"})
	assert.NilError(t, err)
	assert.Equal(t, got, "Bearer abc123")
}

func TestRender_ResolverError(t *testing.T) {
	tpl, err := templates.Parse("${env:MISSING}")
	assert.NilError(t, err)
	_, err = tpl.Render(context.Background(), staticResolver{})
	assert.ErrorContains(t, err, "MISSING")
}

func TestJSONRoundTripLiteral(t *testing.T) {
	var tpl templates.TemplateString
	assert.NilError(t, json.Unmarshal([]byte(`"plain text"`), &tpl))
	assert.Assert(t, tpl.IsLiteral())
	out, err := json.Marshal(tpl)
	assert.NilError(t, err)
	assert.Equal(t, string(out), `"plain text"`)
}

func TestJSONUnmarshalParsesRefs(t *testing.T) {
	var tpl templates.TemplateString
	assert.NilError(t, json.Unmarshal([]byte(`"${env:FOO}-${env:BAR}"`), &tpl))
	assert.Assert(t, !tpl.IsLiteral())
	assert.DeepEqual(t, tpl.References(), []templates.Reference{
		{Type: "env", Name: "FOO"},
		{Type: "env", Name: "BAR"},
	})
}

func TestJSONMarshalUnrenderedFails(t *testing.T) {
	var tpl templates.TemplateString
	assert.NilError(t, json.Unmarshal([]byte(`"${env:FOO}"`), &tpl))
	_, err := json.Marshal(tpl)
	assert.ErrorContains(t, err, "cannot marshal unrendered template")
}

func TestMergeReferences(t *testing.T) {
	a := []templates.Reference{{Type: "env", Name: "A"}, {Type: "env", Name: "B"}}
	b := []templates.Reference{{Type: "env", Name: "B"}, {Type: "sops", Name: "C"}}
	got := templates.MergeReferences(a, b)
	assert.DeepEqual(t, got, []templates.Reference{
		{Type: "env", Name: "A"},
		{Type: "env", Name: "B"},
		{Type: "sops", Name: "C"},
	})
}
