// Package systemsandbox owns system-container runtime integration, immutable
// image planning, and project-scoped mutable cache contracts.
package systemsandbox

// RuntimeName identifies a supported external container runtime CLI.
type RuntimeName string

const (
	Docker  RuntimeName = "docker"
	Podman  RuntimeName = "podman"
	Nerdctl RuntimeName = "nerdctl"
)

// RuntimeCandidates returns system runtime candidates in probe order.
func RuntimeCandidates() []RuntimeName {
	return []RuntimeName{Docker, Podman, Nerdctl}
}
