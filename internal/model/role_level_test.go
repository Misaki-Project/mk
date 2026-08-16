package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoleTargetManualLevel(t *testing.T) {
	assert.Equal(t, RoleTarget("manualLevel"), RoleTargetManualLevel)
	assert.NotEqual(t, RoleTargetManual, RoleTargetManualLevel)
}

func TestParseLevelPolicies(t *testing.T) {
	valid := `{"baseLevel":10,"experiencePolicies":[` +
		`{"level":5,"type":"const","base":100},` +
		`{"level":3,"type":"linear","base":100,"additional":50},` +
		`{"level":2,"type":"exponential","base":50,"additional":25,"exponential":2}]}`

	got, err := ParseLevelPolicies([]byte(valid))
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, 10, got.BaseLevel)
	require.Len(t, got.ExperiencePolicies, 3)

	c := got.ExperiencePolicies[0]
	assert.Equal(t, 5, c.Level)
	assert.Equal(t, string(ExperiencePolicyConst), c.Type)
	assert.Equal(t, 100.0, c.Base)
	assert.Nil(t, c.Additional)

	l := got.ExperiencePolicies[1]
	assert.Equal(t, string(ExperiencePolicyLinear), l.Type)
	require.NotNil(t, l.Additional)
	assert.Equal(t, 50.0, *l.Additional)

	e := got.ExperiencePolicies[2]
	assert.Equal(t, string(ExperiencePolicyExponential), e.Type)
	require.NotNil(t, e.Exponential)
	assert.Equal(t, 2.0, *e.Exponential)
}

func TestParseLevelPolicies_Errors(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{name: "empty", raw: ""},
		{name: "invalid JSON", raw: `{invalid`},
		{name: "not object", raw: `[1,2]`},
		{name: "missing baseLevel", raw: `{"experiencePolicies":[]}`},
		{name: "missing experiencePolicies", raw: `{"baseLevel":0}`},
		{name: "baseLevel not number", raw: `{"baseLevel":"x","experiencePolicies":[]}`},
		{name: "experiencePolicies not array", raw: `{"baseLevel":0,"experiencePolicies":{}}`},
		{name: "policy type unknown", raw: `{"baseLevel":0,"experiencePolicies":[{"level":1,"type":"sqrt","base":10}]}`},
		{name: "policy level not number", raw: `{"baseLevel":0,"experiencePolicies":[{"level":"a","type":"const","base":10}]}`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseLevelPolicies([]byte(tt.raw))
			require.Error(t, err)
			assert.Nil(t, got)
		})
	}
}

func TestParseLevelPolicies_EmptyPolicies(t *testing.T) {
	got, err := ParseLevelPolicies([]byte(`{"baseLevel":0,"experiencePolicies":[]}`))
	require.NoError(t, err)
	assert.Empty(t, got.ExperiencePolicies)
}
