package ratio_setting

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetModelUserGroupRatio(t *testing.T) {
	original := ModelUserGroupRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, UpdateModelUserGroupRatioByJSONString(original))
	})

	require.NoError(t, UpdateModelUserGroupRatioByJSONString(`{
		"enterprise_a": {"*": 0.6, "glm-5.2": 0.8},
		"vip": {"deepseek-v4-flash": 0.3}
	}`))

	tests := []struct {
		name      string
		userGroup string
		modelName string
		wantRatio float64
		wantFound bool
	}{
		{name: "exact model wins", userGroup: "enterprise_a", modelName: "glm-5.2", wantRatio: 0.8, wantFound: true},
		{name: "wildcard fallback", userGroup: "enterprise_a", modelName: "other-model", wantRatio: 0.6, wantFound: true},
		{name: "exact without wildcard", userGroup: "vip", modelName: "deepseek-v4-flash", wantRatio: 0.3, wantFound: true},
		{name: "missing model", userGroup: "vip", modelName: "glm-5.2", wantRatio: 1, wantFound: false},
		{name: "missing group", userGroup: "default", modelName: "glm-5.2", wantRatio: 1, wantFound: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ratio, found := GetModelUserGroupRatio(test.userGroup, test.modelName)
			assert.Equal(t, test.wantRatio, ratio)
			assert.Equal(t, test.wantFound, found)
		})
	}
}

func TestUpdateModelUserGroupRatioRejectsInvalidRatioWithoutReplacingConfig(t *testing.T) {
	original := ModelUserGroupRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, UpdateModelUserGroupRatioByJSONString(original))
	})

	require.NoError(t, UpdateModelUserGroupRatioByJSONString(`{"enterprise_a":{"glm-5.2":0.8}}`))
	require.Error(t, UpdateModelUserGroupRatioByJSONString(`{"enterprise_a":{"glm-5.2":-0.1}}`))

	ratio, found := GetModelUserGroupRatio("enterprise_a", "glm-5.2")
	assert.True(t, found)
	assert.Equal(t, 0.8, ratio)
}