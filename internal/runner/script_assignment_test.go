package runner

import (
	"bytes"
	"testing"

	"github.com/yusing/shadowtree/internal/recipe"
)

func TestRunSupportsAssignmentBeforeScriptRecipeReference(t *testing.T) {
	source := t.TempDir()
	resolved, err := recipe.Resolve(
		"test",
		recipe.Recipe{
			Cmd:       recipe.ScriptCommand("FOO=bar @child alpha beta"),
			Sandboxed: new(recipe.SandboxModeHost),
		},
		nil,
		nil,
		nil,
		"",
		"",
	)
	if err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	err = Run(t.Context(), Options{
		Resolved: resolved,
		Recipes: map[string]recipe.Recipe{
			"child": {Cmd: recipe.Command{"sh", "-c", `printf 'FOO=%s ARGS=%s/%s' "$FOO" "$1" "$2"`, "shadow", "{@}"}},
		},
		SourceDir: source,
		Stdout:    &stdout,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "FOO=bar ARGS=alpha/beta" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}
