package configfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/shadowtree/internal/recipe"
)

func TestLoadTOML(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".shadowtree.toml")
	if err := os.WriteFile(path, []byte(`
profile = "go"
shell = "bash"
shell_prelude = "set -eu"

[vars]
go_ldflags = "-s -w"

[var_commands]
project_root = "pwd"

[recipes.test]
help = "Run tests."
sandboxed = false
cmd = "go test \"$@\""
default_args = ["./..."]
pre = [["go", "generate", "./..."]]

[recipes.test.arguments.count]
help = "Repeat count."
type = "int"
default = 1
`), 0o644); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Config.Profile != "go" {
		t.Fatalf("Profile = %q", loaded.Config.Profile)
	}
	if got := loaded.Config.Recipes["test"].Pre[0][1]; got != "generate" {
		t.Fatalf("pre command = %#v", loaded.Config.Recipes["test"].Pre)
	}
	if got := loaded.Config.Shell; got != "bash" {
		t.Fatalf("Shell = %q", got)
	}
	if got := loaded.Config.ShellPrelude; got != "set -eu" {
		t.Fatalf("ShellPrelude = %q", got)
	}
	if got := loaded.Config.Vars["go_ldflags"]; got != "-s -w" {
		t.Fatalf("go_ldflags = %q", got)
	}
	if got := loaded.Config.VarCommands["project_root"]; len(got) != 2 || got[0] != "__shadowtree_script__" {
		t.Fatalf("project_root command = %#v", got)
	}
	if got := loaded.Config.Recipes["test"].Help; got != "Run tests." {
		t.Fatalf("Help = %q", got)
	}
	if got := loaded.Config.Recipes["test"].Sandboxed; got == nil || *got {
		t.Fatalf("Sandboxed = %#v, want false", got)
	}
	if got := loaded.Config.Recipes["test"].Cmd; len(got) != 2 || got[0] != "__shadowtree_script__" {
		t.Fatalf("cmd = %#v", got)
	}
	if got := loaded.Config.Recipes["test"].Arguments["count"].Default; got == nil {
		t.Fatal("count default is nil")
	}
}

func TestLoadBareRecipeReferenceCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".shadowtree.toml")
	if err := os.WriteFile(path, []byte(`
[recipes.test]
cmd = ["true"]
pre = ["echo 123", "@foo"]

[recipes.foo]
cmd = ["true"]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := recipe.Resolve("test", loaded.Config.Recipes["test"], nil, nil, nil, loaded.Path, "")
	if err != nil {
		t.Fatal(err)
	}
	pre := resolved.Recipe.Pre
	if len(pre) != 2 {
		t.Fatalf("pre = %#v, want two commands", pre)
	}
	if len(pre[0]) != 4 || pre[0][0] != "__shadowtree_script__" || pre[0][1] != "sh" || pre[0][2] != "echo 123" {
		t.Fatalf("pre[0] = %#v, want shell-wrapped script command", pre[0])
	}
	if len(pre[1]) != 1 || pre[1][0] != "@foo" {
		t.Fatalf("pre[1] = %#v, want recipe reference", pre[1])
	}
}

func TestLoadRejectsUnsupportedExtension(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shadowtree.json")
	if err := os.WriteFile(path, []byte(`{"profile":"go"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "unsupported config extension: .json") {
		t.Fatalf("Load() error = %v, want unsupported extension", err)
	}
}
