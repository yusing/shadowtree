package detect

import (
	"os"
	"path/filepath"
	"strings"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing/filemode"
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

// SuperprojectRoot returns the working-tree root of root's registered
// superproject. It returns false when root is not a registered submodule.
func SuperprojectRoot(root string) (string, bool) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}

	superproject := RepoRoot(filepath.Dir(root))
	if superproject == "" || !isAncestor(superproject, root) {
		return "", false
	}

	repo, err := git.PlainOpen(superproject)
	if err != nil {
		return "", false
	}
	defer func() { _ = repo.Close() }()

	worktree, err := repo.Worktree()
	if err != nil {
		return "", false
	}
	submodules, err := worktree.Submodules()
	if err != nil {
		return "", false
	}
	index, err := repo.Storer.Index()
	if err != nil {
		return "", false
	}
	for _, submodule := range submodules {
		path, err := filepath.Abs(filepath.Join(superproject, submodule.Config().Path))
		if err != nil || path != root {
			continue
		}
		entry, err := index.Entry(submodule.Config().Path)
		if err == nil && entry.Mode == filemode.Submodule {
			return superproject, true
		}
	}

	return "", false
}

func isAncestor(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != "." && rel != ".." &&
		!filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
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
