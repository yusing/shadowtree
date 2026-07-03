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

[recipes.test]
help = "Run tests."
cmd = ["go", "test"]
default_args = ["./..."]
pre = [["go", "generate", "./..."]]
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
	if got := loaded.Config.Recipes["test"].Help; got != "Run tests." {
		t.Fatalf("Help = %q", got)
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
}
