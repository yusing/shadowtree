package runner

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yusing/shadowtree/internal/recipe"
	"github.com/yusing/shadowtree/internal/systemsandbox"
)

func TestSystemCacheInspectsPlannedRecipeWithoutMutatingRuntime(t *testing.T) {
	bin := t.TempDir()
	writeExecutable(t, bin, "docker", `
case "$*" in
  "build --help") printf '%s' '--file --tag --label --platform --secret --build-arg' ;;
  "volume create --help") printf '%s' '--label' ;;
  "volume inspect --help") printf '%s' '--format' ;;
  "volume ls --help"|"ps --help") printf '%s' '--filter --format' ;;
  "volume rm --help") printf '%s' ok ;;
  "create --help") printf '%s' '--mount --volume --read-only --user --userns --platform --name --interactive' ;;
  "start --help") printf '%s' '--attach --interactive' ;;
  "kill --help") printf '%s' '--signal' ;;
  "rm --help") printf '%s' '--force' ;;
  "info --format {{json .SecurityOptions}}") printf '%s' '[]' ;;
  "image inspect --help"|"image tag --help"|"info") printf '%s' ok ;;
  volume\ inspect*) printf '%s' 'no such volume' >&2; exit 1 ;;
  *) printf 'unexpected mutation: %s\n' "$*" >&2; exit 1 ;;
esac`)
	t.Setenv("PATH", bin)
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "go.mod"), []byte("module example.com/app\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := recipe.Recipe{Cmd: recipe.Command{"go", "test"}, Sandboxed: new(recipe.SandboxModeSystem)}
	var stdout bytes.Buffer
	if err := SystemCache(t.Context(), SystemCacheOptions{
		Action: "inspect", Recipe: "test", JSON: true, Recipes: map[string]recipe.Recipe{"test": rec},
		Profile: recipe.GoProfile, SourceDir: source, Stdout: &stdout, Stderr: io.Discard,
	}); err != nil {
		t.Fatal(err)
	}
	var document struct {
		Schema string                          `json:"schema"`
		Caches []systemsandbox.CacheInspection `json:"caches"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document.Schema != "shadowtree/cache-inspect/v1" || len(document.Caches) != 1 || document.Caches[0].Provider != "go-build" || document.Caches[0].Exists {
		t.Fatalf("document = %#v", document)
	}
}

func TestResolvedCachePlansReportsSiblingRecipesAndAllowsCommandOnlyRequiredArguments(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "go.mod"), []byte("module example.com/app\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	system := new(recipe.SandboxModeSystem)
	recipes := map[string]recipe.Recipe{
		"test": {
			Cmd: recipe.Command{"go", "test", "{package}"}, Sandboxed: system,
			Arguments: map[string]recipe.Argument{"package": {Type: "string", Required: true}},
		},
		"build": {Cmd: recipe.Command{"go", "build"}, Sandboxed: system},
	}
	plans, shared, err := resolvedCachePlans(t.Context(), SystemCacheOptions{
		Recipe: "test", Recipes: recipes, Profile: recipe.GoProfile, SourceDir: source,
		Confinement: systemsandbox.ConfinementPolicy{User: "1000:998"},
	}, source)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 {
		t.Fatalf("plans = %#v, want one selected cache", plans)
	}
	names := shared[plans[0].Key]
	if len(names) != 2 || !slices.Contains(names, "test") || !slices.Contains(names, "build") {
		t.Fatalf("shared recipes = %#v, want test and build", names)
	}
}
