package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yusing/shadowtree/internal/recipe"
	"github.com/yusing/shadowtree/internal/systemsandbox"
)

// SystemCacheOptions describes one cache inspection or reset command.
type SystemCacheOptions struct {
	Action      string
	Recipe      string
	All         bool
	JSON        bool
	Recipes     map[string]recipe.Recipe
	EnumSets    map[string]recipe.Command
	ConfigEnv   map[string]string
	ConfigPath  string
	Profile     string
	SourceDir   string
	Stdout      io.Writer
	Stderr      io.Writer
	Verbose     bool
	Confinement systemsandbox.ConfinementPolicy
}

// SystemCache executes the read-only inspection or explicitly requested reset
// surface for project-owned runtime caches.
func SystemCache(ctx context.Context, options SystemCacheOptions) error {
	source, err := filepath.Abs(options.SourceDir)
	if err != nil {
		return err
	}
	requestedSource := source
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return fmt.Errorf("canonical cache project: %w", err)
	}
	detectionLog := io.Discard
	if options.Verbose {
		detectionLog = options.Stderr
	}
	runtimeSelection, err := systemsandbox.Detect(ctx, detectionLog)
	if err != nil {
		return err
	}
	runtimeName := runtimeSelection.Name
	options.Confinement = runtimeSelection.Confinement
	if options.All {
		if options.Action != "reset" {
			return errors.New("--all is valid only for cache reset")
		}
		return systemsandbox.ResetProjectCaches(ctx, runtimeName, source, options.Stderr)
	}
	options.SourceDir = requestedSource
	plans, shared, err := resolvedCachePlans(ctx, options, source)
	if err != nil {
		return err
	}
	if options.Action == "reset" {
		if options.Recipe == "" {
			return errors.New("cache reset requires a recipe or --all")
		}
		for _, plan := range plans {
			if names := shared[plan.Key]; len(names) > 1 {
				slices.Sort(names)
				_, _ = fmt.Fprintf(options.Stderr, "shadowtree: cache %s is shared by recipes: %s\n", plan.Provider, strings.Join(names, ", "))
			}
		}
		return systemsandbox.ResetCaches(ctx, runtimeName, plans, options.Stderr)
	}
	var inspections []systemsandbox.CacheInspection
	if options.Recipe == "" {
		inspections, err = systemsandbox.InspectProjectCaches(ctx, runtimeName, source)
		for index := range inspections {
			inspections[index].Recipes = slices.Clone(shared[inspections[index].Key])
			slices.Sort(inspections[index].Recipes)
			if len(inspections[index].Recipes) == 0 {
				inspections[index].Diagnostics = append(inspections[index].Diagnostics, "stale: no currently resolved system recipe uses this cache identity")
			}
		}
	} else {
		inspections, err = systemsandbox.InspectCaches(ctx, runtimeName, plans, shared)
		for index := range inspections {
			inspections[index].ResetCommand = "shadowtree cache reset " + options.Recipe
		}
	}
	if err != nil {
		return err
	}
	if options.JSON {
		encoder := json.NewEncoder(options.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(struct {
			Schema string                          `json:"schema"`
			Caches []systemsandbox.CacheInspection `json:"caches"`
		}{Schema: "shadowtree/cache-inspect/v1", Caches: inspections})
	}
	printCacheInspections(options.Stdout, inspections)
	return nil
}

func resolvedCachePlans(ctx context.Context, options SystemCacheOptions, source string) ([]systemsandbox.CachePlan, map[string][]string, error) {
	if options.Recipe != "" {
		if _, ok := options.Recipes[options.Recipe]; !ok {
			return nil, nil, fmt.Errorf("unknown recipe: %s", options.Recipe)
		}
	}
	byKey := map[string]systemsandbox.CachePlan{}
	shared := map[string][]string{}
	for name, rec := range options.Recipes {
		resolved, err := recipe.ResolveWithOptions(name, rec, nil, nil, options.ConfigEnv, options.ConfigPath, options.Profile, recipe.ResolveOptions{
			Recipes: options.Recipes, EnumSets: options.EnumSets, AllowMissingRequiredArguments: true,
		})
		if err != nil {
			if name != options.Recipe {
				continue
			}
			return nil, nil, err
		}
		if resolved.SandboxMode != recipe.SandboxModeSystem {
			if name != options.Recipe {
				continue
			}
			return nil, nil, fmt.Errorf("recipe %q does not use sandboxed = %q", name, recipe.SandboxModeSystem)
		}
		resolved.ConfigPath, err = rebaseSystemConfigPath(resolved.ConfigPath, options.SourceDir, source)
		if err != nil {
			if options.Recipe == "" || name != options.Recipe {
				continue
			}
			return nil, nil, err
		}
		request, err := resolvedSystemImageRequest(ctx, Options{
			Resolved: resolved, Recipes: options.Recipes, EnumSets: options.EnumSets,
			ConfigEnv: options.ConfigEnv, SourceDir: source,
		})
		if err != nil {
			if options.Recipe == "" || name != options.Recipe {
				continue
			}
			return nil, nil, err
		}
		image, err := systemsandbox.PlanComposition(request, source)
		if err != nil {
			if options.Recipe == "" || name != options.Recipe {
				continue
			}
			return nil, nil, err
		}
		image, err = systemsandbox.ApplyConfinementPolicy(image, options.Confinement)
		if err != nil {
			if options.Recipe == "" || name != options.Recipe {
				continue
			}
			return nil, nil, err
		}
		for _, plan := range image.Caches {
			shared[plan.Key] = append(shared[plan.Key], name)
			if options.Recipe == "" || name == options.Recipe {
				byKey[plan.Key] = plan
			}
		}
	}
	keys := slices.Sorted(maps.Keys(byKey))
	plans := make([]systemsandbox.CachePlan, 0, len(keys))
	for _, key := range keys {
		plans = append(plans, byKey[key])
	}
	return plans, shared, nil
}

func printCacheInspections(w io.Writer, inspections []systemsandbox.CacheInspection) {
	if len(inspections) == 0 {
		_, _ = fmt.Fprintln(w, "No project caches found.")
		return
	}
	for index, inspection := range inspections {
		if index > 0 {
			_, _ = fmt.Fprintln(w)
		}
		_, _ = fmt.Fprintf(w, "%s (%s)\n", inspection.Provider, inspection.Name)
		_, _ = fmt.Fprintf(w, "  runtime: %s\n  exists: %t\n  active: %t\n", inspection.Runtime, inspection.Exists, inspection.Active)
		_, _ = fmt.Fprintf(w, "  project: %s\n  workspace: %s\n", inspection.ProjectRoot, inspection.WorkspaceRoot)
		_, _ = fmt.Fprintf(w, "  key: %s\n  format: %s\n  platform: %s\n", inspection.Key, inspection.Format, inspection.Platform)
		_, _ = fmt.Fprintf(w, "  toolchain: %s\n  abi: %s\n  uid/gid: %d:%d\n", inspection.Toolchain, inspection.ABIKey, inspection.UID, inspection.GID)
		_, _ = fmt.Fprintf(w, "  mount: %s\n  concurrency: %s\n", inspection.MountPath, inspection.Concurrency)
		if len(inspection.Recipes) > 0 {
			_, _ = fmt.Fprintf(w, "  recipes: %s\n", strings.Join(inspection.Recipes, ", "))
		}
		_, _ = fmt.Fprintln(w, "  size: unknown")
		for _, diagnostic := range inspection.Diagnostics {
			_, _ = fmt.Fprintf(w, "  diagnostic: %s\n", diagnostic)
		}
		_, _ = fmt.Fprintf(w, "  reset: %s\n  native: %s\n", inspection.ResetCommand, inspection.NativeCommand)
	}
}
