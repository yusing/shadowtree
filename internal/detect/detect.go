package detect

import (
	"os"
	"path/filepath"
)

const GoProfile = "go"

func Profile(cwd string) string {
	if hasFileUpward(cwd, "go.mod") || hasFileUpward(cwd, "go.work") {
		return GoProfile
	}
	return ""
}

func RepoRoot(cwd string) string {
	root := cwd
	for {
		if exists(filepath.Join(root, ".git")) {
			return root
		}
		parent := filepath.Dir(root)
		if parent == root {
			return cwd
		}
		root = parent
	}
}

func hasFileUpward(cwd, name string) bool {
	dir := cwd
	for {
		if info, err := os.Stat(filepath.Join(dir, name)); err == nil && !info.IsDir() {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
