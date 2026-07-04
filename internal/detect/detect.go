package detect

import (
	"os"
	"path/filepath"
)

const (
	GoProfile   = "go"
	NodeProfile = "node"
)

func Profile(cwd string) string {
	profile, ok := nearestProfileMarker(cwd)
	if !ok {
		return ""
	}
	return profile
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

func nearestProfileMarker(cwd string) (string, bool) {
	dir := cwd
	for {
		hasGo := hasFile(dir, "go.mod") || hasFile(dir, "go.work")
		hasNode := hasFile(dir, "package.json")
		switch {
		case hasGo:
			return GoProfile, true
		case hasNode:
			return NodeProfile, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func hasFile(dir, name string) bool {
	info, err := os.Stat(filepath.Join(dir, name))
	return err == nil && !info.IsDir()
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
