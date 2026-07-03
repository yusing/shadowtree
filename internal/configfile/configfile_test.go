package configfile

import (
	"os"
	"path/filepath"
	"testing"
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

func TestLoadYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".shadowtree.yaml")
	if err := os.WriteFile(path, []byte(`
profile: go
recipes:
  test:
    help: Run tests.
    cmd: [go, test]
    default_args: [./...]
    arguments:
      race:
        help: Enable race detector.
        type: bool
        default: false
`), 0o644); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Config.Recipes["test"].Cmd[0]; got != "go" {
		t.Fatalf("cmd[0] = %q", got)
	}
	if got := loaded.Config.Recipes["test"].Help; got != "Run tests." {
		t.Fatalf("Help = %q", got)
	}
	if got := loaded.Config.Recipes["test"].Arguments["race"].Type; got != "bool" {
		t.Fatalf("race type = %q", got)
	}
}
