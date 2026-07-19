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

func TestProfileEnumMatchesRuntimeProfiles(t *testing.T) {
	data, err := os.ReadFile("shadowtree.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	properties := schemaObject(t, schema, "properties")
	profile := schemaObject(t, properties, "profile")
	values, ok := profile["enum"].([]any)
	if !ok {
		t.Fatalf("profile enum = %#v", profile["enum"])
	}
	got := make([]string, 0, len(values))
	for _, value := range values {
		got = append(got, value.(string))
	}
	want := []string{recipe.GoProfile, recipe.NodeProfile, recipe.RustProfile}
	if !slices.Equal(got, want) {
		t.Fatalf("profile enum = %#v, want %#v", got, want)
	}
}

func TestSandboxedSchemaHasBooleanAndSystemContract(t *testing.T) {
	data, err := os.ReadFile("shadowtree.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	definitions := schemaObject(t, schema, "definitions")
	recipeDefinition := schemaObject(t, definitions, "recipe")
	properties := schemaObject(t, recipeDefinition, "properties")
	sandboxed := schemaObject(t, properties, "sandboxed")
	options, ok := sandboxed["oneOf"].([]any)
	if !ok || len(options) != 2 {
		t.Fatalf("sandboxed oneOf = %#v, want boolean and system", sandboxed["oneOf"])
	}
	if first := options[0].(map[string]any)["type"]; first != "boolean" {
		t.Fatalf("sandboxed first option = %#v", options[0])
	}
	if second := options[1].(map[string]any)["const"]; second != "system" {
		t.Fatalf("sandboxed second option = %#v", options[1])
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
