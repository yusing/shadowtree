package recipe

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultGoToolchain is the release-pinned system-image default.
const DefaultGoToolchain = "1.26.4"

// GoToolchainInfo is the canonical system-image Go release selection.
type GoToolchainInfo struct {
	Version    string
	Provenance string
}

// ResolveGoToolchain selects an exact release without invoking the host Go
// toolchain. A project minor directive uses Shadowtree's pinned patch release.
func ResolveGoToolchain(dir, boundary string) (GoToolchainInfo, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return GoToolchainInfo{}, err
	}
	limit, err := filepath.Abs(boundary)
	if err != nil {
		return GoToolchainInfo{}, err
	}
	if rel, err := filepath.Rel(limit, root); err != nil || !filepath.IsLocal(rel) {
		return GoToolchainInfo{}, fmt.Errorf("go workdir %q is outside canonical project %q", root, limit)
	}
	for _, name := range []string{"go.work", "go.mod"} {
		if path := nearestRegularFile(root, limit, name); path != "" {
			toolchain, directive, err := goToolchainDirectives(path)
			if err != nil {
				return GoToolchainInfo{}, err
			}
			if toolchain != "" {
				if !exactGoRelease(toolchain) {
					return GoToolchainInfo{}, fmt.Errorf("unsupported Go toolchain in %s: %q; use an exact release such as go%s", path, toolchain, DefaultGoToolchain)
				}
				return GoToolchainInfo{Version: strings.TrimPrefix(toolchain, "go"), Provenance: path + "#toolchain"}, nil
			}
			if directive != "" {
				version, err := pinnedGoDirectiveVersion(directive)
				if err != nil {
					return GoToolchainInfo{}, fmt.Errorf("go directive in %s: %w", path, err)
				}
				return GoToolchainInfo{Version: version, Provenance: path + "#go"}, nil
			}
		}
	}
	return GoToolchainInfo{Version: DefaultGoToolchain, Provenance: "shadowtree-default"}, nil
}

func nearestRegularFile(dir, boundary, name string) string {
	for current := dir; ; current = filepath.Dir(current) {
		path := filepath.Join(current, name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
		if current == boundary {
			return ""
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
	}
}

func goToolchainDirectives(path string) (toolchain, directive string, err error) {
	file, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "toolchain":
			toolchain = fields[1]
		case "go":
			directive = fields[1]
		}
	}
	return toolchain, directive, scanner.Err()
}

func exactGoRelease(value string) bool {
	value = strings.TrimPrefix(value, "go")
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" || strings.Trim(part, "0123456789") != "" {
			return false
		}
	}
	return true
}

func pinnedGoDirectiveVersion(value string) (string, error) {
	if exactGoRelease(value) {
		return value, nil
	}
	parts := strings.Split(value, ".")
	defaultParts := strings.Split(DefaultGoToolchain, ".")
	if len(parts) == 2 && len(defaultParts) == 3 && parts[0] == defaultParts[0] && parts[1] == defaultParts[1] {
		return DefaultGoToolchain, nil
	}
	return "", fmt.Errorf("unsupported unpinned version %q; use toolchain go%s or system.base_image", value, DefaultGoToolchain)
}
