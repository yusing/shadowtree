package systemsandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yusing/shadowtree/internal/recipe"
)

const cachePlanVersion = "system-cache-v2"

// CachePlan describes one project-owned mutable build cache.
type CachePlan struct {
	Name          string            `json:"name"`
	Key           string            `json:"key"`
	ProjectKey    string            `json:"project_key"`
	ProjectRoot   string            `json:"project_root"`
	WorkspaceRoot string            `json:"workspace_root"`
	Provider      string            `json:"provider"`
	Format        string            `json:"format"`
	Platform      string            `json:"platform"`
	Toolchain     string            `json:"toolchain"`
	ABIKey        string            `json:"abi_key"`
	UID           int               `json:"uid"`
	GID           int               `json:"gid"`
	MountPath     string            `json:"mount_path"`
	OutputPath    string            `json:"output_path,omitempty"`
	Environment   map[string]string `json:"environment,omitempty"`
	Concurrency   string            `json:"concurrency"`
	Labels        map[string]string `json:"labels"`
}

// CacheInspection is the stable machine-readable cache inspection record.
type CacheInspection struct {
	CachePlan
	Runtime       RuntimeName `json:"runtime"`
	Exists        bool        `json:"exists"`
	Active        bool        `json:"active"`
	SizeBytes     *int64      `json:"size_bytes"`
	Recipes       []string    `json:"recipes"`
	Diagnostics   []string    `json:"diagnostics"`
	ResetCommand  string      `json:"reset_command"`
	NativeCommand string      `json:"native_command"`
}

type cacheDescriptor struct {
	provider, format, workspace, mountPath, outputPath, toolchain, concurrency string
	environment                                                                map[string]string
	inputs                                                                     map[string]string
}

func planCaches(descriptors []cacheDescriptor, projectRoot, projectKey, platform string, stages []ImageStage) []CachePlan {
	if len(descriptors) == 0 {
		return nil
	}
	abiInputs := make([]string, 0, min(3, len(stages)))
	for _, stage := range stages[:min(3, len(stages))] {
		abiInputs = append(abiInputs, stage.Key)
	}
	abiKey := digestKey(abiInputs)
	uid, gid := os.Getuid(), os.Getgid()
	plans := make([]CachePlan, 0, len(descriptors))
	for _, descriptor := range descriptors {
		workspaceRoot := projectRoot
		if descriptor.workspace != "" {
			workspaceRoot = filepath.Join(projectRoot, filepath.FromSlash(descriptor.workspace))
		}
		key := cacheCompatibilityKey(projectKey, descriptor.workspace, descriptor.provider, descriptor.format, platform, descriptor.toolchain, abiKey, uid, gid, descriptor.inputs)
		name := "shadowtree-cache-" + key
		inputs, _ := json.Marshal(descriptor.inputs)
		labels := map[string]string{
			"shadowtree.owner":             "github.com/yusing/shadowtree",
			"shadowtree.kind":              "cache",
			"shadowtree.cache-version":     cachePlanVersion,
			"shadowtree.cache-key":         key,
			"shadowtree.project-key":       projectKey,
			"shadowtree.project-root":      projectRoot,
			"shadowtree.workspace-root":    descriptor.workspace,
			"shadowtree.cache-provider":    descriptor.provider,
			"shadowtree.cache-format":      descriptor.format,
			"shadowtree.platform":          platform,
			"shadowtree.toolchain":         descriptor.toolchain,
			"shadowtree.abi-key":           abiKey,
			"shadowtree.uid":               strconv.Itoa(uid),
			"shadowtree.gid":               strconv.Itoa(gid),
			"shadowtree.cache-mount-path":  descriptor.mountPath,
			"shadowtree.cache-output-path": descriptor.outputPath,
			"shadowtree.concurrency":       descriptor.concurrency,
			"shadowtree.cache-inputs":      string(inputs),
		}
		plans = append(plans, CachePlan{
			Name: name, Key: key, ProjectKey: projectKey, ProjectRoot: projectRoot,
			WorkspaceRoot: workspaceRoot, Provider: descriptor.provider, Format: descriptor.format,
			Platform: platform, Toolchain: descriptor.toolchain, ABIKey: abiKey, UID: uid, GID: gid,
			MountPath: descriptor.mountPath, OutputPath: descriptor.outputPath, Environment: descriptor.environment,
			Concurrency: descriptor.concurrency, Labels: labels,
		})
	}
	return plans
}

// ApplyConfinementPolicy updates mutable cache identities to the effective
// in-container UID/GID selected for the runtime without changing image keys.
func ApplyConfinementPolicy(plan ImagePlan, policy ConfinementPolicy) (ImagePlan, error) {
	uidText, gidText, ok := strings.Cut(policy.User, ":")
	if !ok {
		return ImagePlan{}, fmt.Errorf("invalid confinement user identity %q", policy.User)
	}
	uid, err := strconv.Atoi(uidText)
	if err != nil || uid < 0 {
		return ImagePlan{}, fmt.Errorf("invalid confinement UID %q", uidText)
	}
	gid, err := strconv.Atoi(gidText)
	if err != nil || gid < 0 {
		return ImagePlan{}, fmt.Errorf("invalid confinement GID %q", gidText)
	}
	plan.Caches = slices.Clone(plan.Caches)
	for index := range plan.Caches {
		cache := &plan.Caches[index]
		var inputs map[string]string
		if err := json.Unmarshal([]byte(cache.Labels["shadowtree.cache-inputs"]), &inputs); err != nil {
			return ImagePlan{}, fmt.Errorf("cache %s provider inputs: %w", cache.Provider, err)
		}
		workspace := cache.Labels["shadowtree.workspace-root"]
		key := cacheCompatibilityKey(cache.ProjectKey, workspace, cache.Provider, cache.Format, cache.Platform, cache.Toolchain, cache.ABIKey, uid, gid, inputs)
		cache.Key, cache.Name, cache.UID, cache.GID = key, "shadowtree-cache-"+key, uid, gid
		cache.Labels = maps.Clone(cache.Labels)
		cache.Labels["shadowtree.cache-key"] = key
		cache.Labels["shadowtree.uid"] = uidText
		cache.Labels["shadowtree.gid"] = gidText
	}
	return plan, nil
}

func cacheCompatibilityKey(projectKey, workspace, provider, format, platform, toolchain, abiKey string, uid, gid int, inputs map[string]string) string {
	return digestKey(map[string]any{
		"version": cachePlanVersion, "project": projectKey, "workspace": workspace,
		"provider": provider, "format": format, "platform": platform,
		"toolchain": toolchain, "abi": abiKey, "uid": uid, "gid": gid, "inputs": inputs,
	})
}

// CanonicalProjectKey returns the stable ownership key for one canonical root.
func CanonicalProjectKey(projectRoot string) string {
	return digestKey(map[string]any{"root": projectRoot})
}

func validateCachePlan(plan CachePlan) error {
	if plan.Name == "" || plan.Key == "" || plan.ProjectKey == "" || plan.Provider == "" || plan.MountPath == "" {
		return fmt.Errorf("incomplete cache plan for provider %q", plan.Provider)
	}
	if !filepath.IsAbs(plan.MountPath) {
		return fmt.Errorf("cache %s mount path must be absolute", plan.Provider)
	}
	switch plan.Provider {
	case "go-build":
		if plan.Format != "go-build-v1" || plan.MountPath != "/opt/shadowtree/cache/go-build" || plan.OutputPath != "" || plan.Concurrency != "shared" {
			return errors.New("invalid Go build cache provider contract")
		}
	case "cargo-target":
		if plan.Format != "cargo-target-v1" || plan.MountPath != "/opt/shadowtree/cache/cargo-target" || plan.Concurrency != recipe.RustTargetCacheConcurrency {
			return errors.New("invalid Cargo target cache provider contract")
		}
		if rel, err := filepath.Rel(plan.WorkspaceRoot, plan.OutputPath); err != nil || rel != "target" {
			return errors.New("Cargo target cache output must be the workspace target directory")
		}
	default:
		return fmt.Errorf("unsupported cache provider %q", plan.Provider)
	}
	return nil
}

// PrepareCaches creates missing exact-key volumes and rejects ownership or
// compatibility collisions before user code starts.
func PrepareCaches(ctx context.Context, runtime RuntimeName, plans []CachePlan, image string, progress io.Writer) error {
	return prepareCachesWith(ctx, runtime, plans, image, progress, directCommand)
}

// WaitForCacheAvailability waits until exclusive volumes are not in active use
// by another runtime container. Shared providers never wait.
func WaitForCacheAvailability(ctx context.Context, runtime RuntimeName, plans []CachePlan, progress io.Writer) error {
	for _, plan := range plans {
		if plan.Concurrency != "exclusive" {
			continue
		}
		waiting := false
		for {
			active, err := cacheVolumeActive(ctx, runtime, plan.Name, directCommand)
			if err != nil {
				return err
			}
			if !active {
				break
			}
			if !waiting && progress != nil {
				fmt.Fprintf(progress, "shadowtree: waiting for runtime cache %s\n", plan.Provider)
				waiting = true
			}
			select {
			case <-ctx.Done():
				return context.Cause(ctx)
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
	return nil
}

func prepareCachesWith(ctx context.Context, runtime RuntimeName, plans []CachePlan, image string, progress io.Writer, run commandRunner) error {
	if progress == nil {
		progress = io.Discard
	}
	for _, plan := range plans {
		if err := validateCachePlan(plan); err != nil {
			return err
		}
		fmt.Fprintf(progress, "shadowtree: cache %s lookup\n", plan.Provider)
		labels, exists, err := inspectVolumeLabels(ctx, runtime, plan.Name, run)
		if err != nil {
			return fmt.Errorf("runtime %s cache %s lookup: %w", runtime, plan.Provider, err)
		}
		if exists {
			if !labelsMatch(labels, plan.Labels) {
				return fmt.Errorf("runtime %s cache %s volume collision at %s; inspect or remove the conflicting volume", runtime, plan.Provider, plan.Name)
			}
			fmt.Fprintf(progress, "shadowtree: cache %s reused\n", plan.Provider)
			continue
		}
		fmt.Fprintf(progress, "shadowtree: cache %s create\n", plan.Provider)
		args := []string{"volume", "create"}
		keys := slices.Sorted(maps.Keys(plan.Labels))
		for _, key := range keys {
			args = append(args, "--label", key+"="+plan.Labels[key])
		}
		args = append(args, plan.Name)
		output, err := run(ctx, string(runtime), args...)
		if err != nil {
			return fmt.Errorf("runtime %s cache %s create: %s", runtime, plan.Provider, commandFailure(err, output))
		}
		labels, exists, err = inspectVolumeLabels(ctx, runtime, plan.Name, run)
		if err != nil || !exists || !labelsMatch(labels, plan.Labels) {
			return fmt.Errorf("runtime %s cache %s create did not publish the requested labelled volume", runtime, plan.Provider)
		}
		fmt.Fprintf(progress, "shadowtree: cache %s initialize ownership\n", plan.Provider)
		output, err = run(ctx, string(runtime), "run", "--rm", "--network", "none", "--read-only", "--user", "0:0", "--entrypoint", "/bin/sh", "--volume", plan.Name+":/opt/shadowtree/cache-init", image, "-c", fmt.Sprintf("chown -R %d:%d /opt/shadowtree/cache-init && chmod u+rwx /opt/shadowtree/cache-init", plan.UID, plan.GID))
		if err != nil {
			return fmt.Errorf("runtime %s cache %s initialize ownership: %s", runtime, plan.Provider, commandFailure(err, output))
		}
	}
	return nil
}

// InspectCaches inspects planned cache identities without mounting or mutating
// volumes. recipes maps cache keys to same-project recipes sharing the cache.
func InspectCaches(ctx context.Context, runtime RuntimeName, plans []CachePlan, recipes map[string][]string) ([]CacheInspection, error) {
	return inspectCachesWith(ctx, runtime, plans, recipes, directCommand)
}

// InspectProjectCaches lists only volumes labelled for the current canonical
// project, then validates their complete cache ownership metadata.
func InspectProjectCaches(ctx context.Context, runtime RuntimeName, projectRoot string) ([]CacheInspection, error) {
	return inspectProjectCachesWith(ctx, runtime, projectRoot, directCommand)
}

func inspectProjectCachesWith(ctx context.Context, runtime RuntimeName, projectRoot string, run commandRunner) ([]CacheInspection, error) {
	projectKey := CanonicalProjectKey(projectRoot)
	output, err := run(ctx, string(runtime), "volume", "ls",
		"--filter", "label=shadowtree.owner=github.com/yusing/shadowtree",
		"--filter", "label=shadowtree.kind=cache",
		"--filter", "label=shadowtree.project-key="+projectKey,
		"--format", "{{.Name}}")
	if err != nil {
		return nil, fmt.Errorf("runtime %s list project caches: %s", runtime, commandFailure(err, output))
	}
	var inspections []CacheInspection
	for line := range strings.Lines(string(output)) {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		labels, exists, err := inspectVolumeLabels(ctx, runtime, name, run)
		if err != nil {
			return nil, err
		}
		if !exists {
			continue
		}
		plan, diagnostics := cachePlanFromLabels(name, projectRoot, labels)
		inspection := CacheInspection{
			CachePlan: plan, Runtime: runtime, Exists: true, SizeBytes: nil, Diagnostics: diagnostics,
			ResetCommand: "shadowtree cache reset --all", NativeCommand: string(runtime) + " volume inspect " + name,
		}
		active, err := cacheVolumeActive(ctx, runtime, name, run)
		if err != nil {
			return nil, err
		}
		inspection.Active = active
		inspections = append(inspections, inspection)
	}
	slices.SortFunc(inspections, func(a, b CacheInspection) int { return strings.Compare(a.Name, b.Name) })
	return inspections, nil
}

func inspectCachesWith(ctx context.Context, runtime RuntimeName, plans []CachePlan, recipes map[string][]string, run commandRunner) ([]CacheInspection, error) {
	inspections := make([]CacheInspection, 0, len(plans))
	for _, plan := range plans {
		inspection := CacheInspection{
			CachePlan: plan, Runtime: runtime, SizeBytes: nil, Recipes: slices.Clone(recipes[plan.Key]),
			ResetCommand:  "shadowtree cache reset " + plan.Provider,
			NativeCommand: string(runtime) + " volume inspect " + plan.Name,
		}
		labels, exists, err := inspectVolumeLabels(ctx, runtime, plan.Name, run)
		if err != nil {
			return nil, fmt.Errorf("inspect cache %s: %w", plan.Provider, err)
		}
		inspection.Exists = exists
		if exists && !labelsMatch(labels, plan.Labels) {
			inspection.Diagnostics = append(inspection.Diagnostics, "volume name exists with incompatible or malformed ownership labels")
		}
		if exists {
			active, err := cacheVolumeActive(ctx, runtime, plan.Name, run)
			if err != nil {
				return nil, err
			}
			inspection.Active = active
		}
		slices.Sort(inspection.Recipes)
		inspections = append(inspections, inspection)
	}
	return inspections, nil
}

// ResetCaches removes exact, inactive, label-validated cache volumes. Missing
// volumes are treated idempotently.
func ResetCaches(ctx context.Context, runtime RuntimeName, plans []CachePlan, progress io.Writer) error {
	return resetCachesWith(ctx, runtime, plans, progress, directCommand)
}

// ResetProjectCaches removes all fully labelled inactive cache volumes owned by
// the current canonical project, and nothing machine-wide.
func ResetProjectCaches(ctx context.Context, runtime RuntimeName, projectRoot string, progress io.Writer) error {
	inspections, err := InspectProjectCaches(ctx, runtime, projectRoot)
	if err != nil {
		return err
	}
	plans := make([]CachePlan, 0, len(inspections))
	for _, inspection := range inspections {
		if len(inspection.Diagnostics) > 0 {
			return fmt.Errorf("refusing project cache reset: volume %s has malformed ownership metadata: %s", inspection.Name, strings.Join(inspection.Diagnostics, "; "))
		}
		plans = append(plans, inspection.CachePlan)
	}
	return ResetCaches(ctx, runtime, plans, progress)
}

func resetCachesWith(ctx context.Context, runtime RuntimeName, plans []CachePlan, progress io.Writer, run commandRunner) error {
	release, err := acquireCacheResetLocks(ctx, plans, progress)
	if err != nil {
		return err
	}
	defer release()
	for _, plan := range plans {
		labels, exists, err := inspectVolumeLabels(ctx, runtime, plan.Name, run)
		if err != nil {
			return fmt.Errorf("inspect cache %s before reset: %w", plan.Provider, err)
		}
		if !exists {
			continue
		}
		if !labelsMatch(labels, plan.Labels) {
			return fmt.Errorf("refusing to reset cache %s: volume %s has incompatible ownership labels", plan.Provider, plan.Name)
		}
		active, err := cacheVolumeActive(ctx, runtime, plan.Name, run)
		if err != nil {
			return err
		}
		if active {
			return fmt.Errorf("refusing to reset active cache %s (%s)", plan.Provider, plan.Name)
		}
		output, err := run(ctx, string(runtime), "volume", "rm", plan.Name)
		if err != nil && !volumeMissing(output) {
			return fmt.Errorf("reset cache %s: %s", plan.Provider, commandFailure(err, output))
		}
	}
	return nil
}

func cacheVolumeActive(ctx context.Context, runtime RuntimeName, name string, run commandRunner) (bool, error) {
	output, err := run(ctx, string(runtime), "ps", "--filter", "volume="+name, "--format", "{{.ID}}")
	if err != nil {
		return false, fmt.Errorf("runtime %s inspect active cache %s: %s", runtime, name, commandFailure(err, output))
	}
	return len(bytes.TrimSpace(output)) > 0, nil
}

func inspectVolumeLabels(ctx context.Context, runtime RuntimeName, name string, run commandRunner) (map[string]string, bool, error) {
	output, err := run(ctx, string(runtime), "volume", "inspect", "--format", "{{json .Labels}}", name)
	if err != nil {
		if volumeMissing(output) {
			return nil, false, nil
		}
		return nil, false, errors.New(commandFailure(err, output))
	}
	labels := map[string]string{}
	if err := json.Unmarshal(bytes.TrimSpace(output), &labels); err != nil {
		return nil, false, fmt.Errorf("parse volume labels: %w", err)
	}
	return labels, true, nil
}

func volumeMissing(output []byte) bool {
	message := strings.ToLower(string(output))
	return strings.Contains(message, "no such volume") || strings.Contains(message, "volume not found") || strings.Contains(message, "no such object")
}

func cachePlanFromLabels(name, projectRoot string, labels map[string]string) (CachePlan, []string) {
	plan := CachePlan{
		Name: name, Key: labels["shadowtree.cache-key"], ProjectKey: labels["shadowtree.project-key"],
		ProjectRoot: labels["shadowtree.project-root"], Provider: labels["shadowtree.cache-provider"],
		Format: labels["shadowtree.cache-format"], Platform: labels["shadowtree.platform"],
		Toolchain: labels["shadowtree.toolchain"], ABIKey: labels["shadowtree.abi-key"],
		MountPath: labels["shadowtree.cache-mount-path"], Concurrency: labels["shadowtree.concurrency"],
		OutputPath: labels["shadowtree.cache-output-path"],
		Labels:     labels,
	}
	workspace := labels["shadowtree.workspace-root"]
	plan.WorkspaceRoot = projectRoot
	if workspace != "" && filepath.IsLocal(filepath.FromSlash(workspace)) {
		plan.WorkspaceRoot = filepath.Join(projectRoot, filepath.FromSlash(workspace))
	}
	plan.UID, _ = strconv.Atoi(labels["shadowtree.uid"])
	plan.GID, _ = strconv.Atoi(labels["shadowtree.gid"])
	var diagnostics []string
	if labels["shadowtree.owner"] != "github.com/yusing/shadowtree" || labels["shadowtree.kind"] != "cache" || labels["shadowtree.cache-version"] != cachePlanVersion {
		diagnostics = append(diagnostics, "unsupported owner, kind, or cache format version")
	}
	if plan.ProjectKey != CanonicalProjectKey(projectRoot) || plan.ProjectRoot != projectRoot {
		diagnostics = append(diagnostics, "project ownership does not match the canonical checkout")
	}
	if workspace != "" && !filepath.IsLocal(filepath.FromSlash(workspace)) {
		diagnostics = append(diagnostics, "workspace ownership escapes the canonical checkout")
	}
	if err := validateCachePlan(plan); err != nil {
		diagnostics = append(diagnostics, err.Error())
	}
	var inputs map[string]string
	if err := json.Unmarshal([]byte(labels["shadowtree.cache-inputs"]), &inputs); err != nil {
		diagnostics = append(diagnostics, "cache provider inputs are malformed")
	} else {
		workspaceRel := labels["shadowtree.workspace-root"]
		wantKey := cacheCompatibilityKey(plan.ProjectKey, workspaceRel, plan.Provider, plan.Format, plan.Platform, plan.Toolchain, plan.ABIKey, plan.UID, plan.GID, inputs)
		if plan.Key != wantKey {
			diagnostics = append(diagnostics, "cache key does not match its compatibility metadata")
		}
	}
	if plan.Name != "shadowtree-cache-"+plan.Key {
		diagnostics = append(diagnostics, "native volume name does not match the complete cache key")
	}
	return plan, diagnostics
}
