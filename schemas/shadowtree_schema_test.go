package schemas_test

import (
	"encoding/json"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/shadowtree/internal/recipe"
)

func TestRequirementPackageMapKeyPatternMatchesRuntimeRules(t *testing.T) {
	pattern := schemaPatternProperty(t, "requirementPackageMap")
	if pattern != `^(?!run_id$)(?!.*[/\\])\S(?:.*\S)?$` {
		t.Fatalf("requirementPackageMap key pattern = %q, want runtime-aligned executable-name pattern", pattern)
	}
}

func TestRecipeNameReservedPatternMatchesRuntimeSources(t *testing.T) {
	pattern := schemaPatternProperty(t, "recipes")
	got := reservedNamesFromRecipePattern(t, pattern)

	wantSet := maps.Clone(recipe.ReservedNames)
	for _, name := range recipe.BuiltinReferenceNames() {
		wantSet[name] = true
	}
	want := slices.Sorted(maps.Keys(wantSet))

	if !slices.Equal(got, want) {
		t.Fatalf("schema reserved recipe names = %#v, want %#v", got, want)
	}
}

func schemaPatternProperty(t *testing.T, definitionName string) string {
	t.Helper()
	data, err := os.ReadFile("shadowtree.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	definitions := schemaObject(t, schema, "definitions")
	definition := schemaObject(t, definitions, definitionName)
	additionalProperties, ok := definition["additionalProperties"].(bool)
	if !ok || additionalProperties {
		t.Fatalf("%s additionalProperties = %#v, want false", definitionName, definition["additionalProperties"])
	}
	patternProperties := schemaObject(t, definition, "patternProperties")
	if len(patternProperties) != 1 {
		t.Fatalf("%s patternProperties has %d entries, want 1", definitionName, len(patternProperties))
	}
	for pattern := range patternProperties {
		return pattern
	}
	t.Fatalf("%s patternProperties is empty", definitionName)
	return ""
}

func schemaObject(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := parent[key]
	if !ok {
		t.Fatalf("missing schema key %q", key)
	}
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("schema key %q has type %T, want object", key, value)
	}
	return object
}

func reservedNamesFromRecipePattern(t *testing.T, pattern string) []string {
	t.Helper()
	const prefix = `^(?!(?:`
	const suffix = `)$).*$`
	if !strings.HasPrefix(pattern, prefix) || !strings.HasSuffix(pattern, suffix) {
		t.Fatalf("recipes key pattern = %q, want reserved-name negative lookahead", pattern)
	}
	names := strings.Split(strings.TrimSuffix(strings.TrimPrefix(pattern, prefix), suffix), "|")
	slices.Sort(names)
	return names
}
