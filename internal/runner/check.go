package runner

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yusing/shadowtree/internal/configfile"
	"github.com/yusing/shadowtree/internal/recipe"
	"github.com/yusing/shadowtree/internal/scriptref"
	"github.com/yusing/shadowtree/internal/systemsandbox"
)

func validatePlan(ctx context.Context, options Options) error {
	return validateResolvedPlan(ctx, options, []string{recipeReferenceStackKey(options.Resolved.ConfigPath, options.Resolved.Name)})
}

func validateResolvedPlan(ctx context.Context, options Options, stack []string) error {
	resolved := options.Resolved
	if err := validateSystemCheck(ctx, options); err != nil {
		return err
	}
	if resolved.LogPath != "" {
		if _, _, _, err := recipeLogPath(resolved, options.SourceDir); err != nil {
			return fmt.Errorf("recipe %q log: %w", resolved.Name, err)
		}
	}
	if resolved.Recipe.Workdir != "" {
		if _, err := recipeWorkdir(options.SourceDir, resolved.Recipe.Workdir); err != nil {
			return fmt.Errorf("recipe %q workdir: %w", resolved.Name, err)
		}
	}
	if forEach := expandedForEachCommand(resolved); len(forEach) > 0 {
		if err := validateCommandReferences(ctx, options, "for_each", forEach, stack); err != nil {
			return err
		}
	}
	for i, command := range resolved.Recipe.Pre {
		if err := validateCommandReferences(ctx, options, fmt.Sprintf("pre[%d]", i), command.Cmd, stack); err != nil {
			return err
		}
	}
	if err := validateCommandReferences(ctx, options, "cmd", resolved.Main, stack); err != nil {
		return err
	}
	for i, command := range resolved.Recipe.Post {
		if err := validateCommandReferences(ctx, options, fmt.Sprintf("post[%d]", i), command.Cmd, stack); err != nil {
			return err
		}
	}
	return nil
}

func validateSystemCheck(ctx context.Context, options Options) error {
	if options.Resolved.SandboxMode != recipe.SandboxModeSystem {
		return nil
	}
	if _, err := systemsandbox.Detect(ctx, options.Stderr); err != nil {
		return fmt.Errorf("recipe %q system runtime detection: %w", options.Resolved.Name, err)
	}
	resolved, err := resolvedSystemImageRecipe(ctx, options)
	if err != nil {
		return wrapSystemImageRequirements(options.Resolved.Name, err)
	}
	if _, err := systemsandbox.PlanImages(resolved, options.SourceDir); err != nil {
		return fmt.Errorf("recipe %q system image plan: %w", options.Resolved.Name, err)
	}
	return nil
}

func validateCommandReferences(ctx context.Context, options Options, stage string, command recipe.Command, stack []string) error {
	if stage == "for_each" {
		if _, ok, err := recipe.ValueBuiltinUsesFilesystem(command); ok {
			return err
		}
	}
	if target, ok, err := retryInvocation(command); ok {
		if err != nil {
			return fmt.Errorf("recipe %q %s: %w", options.Resolved.Name, stage, err)
		}
		if ref, ok := recipeReferenceForValidation(options, target); ok {
			return validateRecipeReference(ctx, options, stage, ref, false, stack)
		}
		return nil
	}
	if ref, ok := recipeReferenceForValidation(options, command); ok {
		return validateRecipeReference(ctx, options, stage, ref, false, stack)
	}
	if !recipe.IsScriptCommand(command) {
		return nil
	}
	shell := recipe.ScriptShell(command)
	_, refs, err := scriptref.Parse(shell, recipe.ScriptBody(command))
	if err != nil {
		if options.CheckShell {
			return fmt.Errorf("recipe %q %s shell: %w", options.Resolved.Name, stage, err)
		}
		return nil
	}
	for _, ref := range refs {
		command := recipe.Command{ref.Value}
		dynamicArgs := false
		for _, arg := range ref.Args {
			if arg.Dynamic {
				dynamicArgs = true
			}
			command = append(command, arg.Value)
		}
		if target, ok, err := retryInvocation(command); ok {
			if err != nil {
				return fmt.Errorf("recipe %q %s: %w", options.Resolved.Name, stage, err)
			}
			if ref, ok := recipeReferenceForValidation(options, target); ok {
				if err := validateRecipeReference(ctx, options, stage, ref, dynamicArgs, stack); err != nil {
					return err
				}
			}
			continue
		}
		if parsed, ok := recipeReferenceForValidation(options, command); ok {
			if err := validateRecipeReference(ctx, options, stage, parsed, dynamicArgs, stack); err != nil {
				return err
			}
		}
	}
	return nil
}

func recipeReferenceForValidation(options Options, command recipe.Command) (recipe.RecipeReferenceTarget, bool) {
	if options.Recipes == nil {
		return recipe.RecipeReferenceTarget{}, false
	}
	return recipe.ParseRecipeReference(command)
}

func validateRecipeReference(ctx context.Context, options Options, stage string, ref recipe.RecipeReferenceTarget, dynamicArgs bool, stack []string) error {
	if ref.Path != "" {
		return validateCrossConfigReference(ctx, options, stage, ref, dynamicArgs, stack)
	}
	key := recipeReferenceStackKey(options.Resolved.ConfigPath, ref.Name)
	if slices.Contains(stack, key) {
		cycle := append(slices.Clone(stack), key)
		return fmt.Errorf("recipe reference cycle: %s", joinReferenceStack(cycle))
	}
	rec, ok := options.Recipes[ref.Name]
	if !ok {
		return fmt.Errorf("recipe %q %s: unknown recipe reference: @%s", options.Resolved.Name, stage, ref.Name)
	}
	if dynamicArgs {
		return nil
	}
	resolved, err := recipe.ResolveWithOptions(ref.Name, rec, ref.Args, nil, options.ConfigEnv, options.Resolved.ConfigPath, options.Resolved.Profile, recipe.ResolveOptions{RunID: options.Resolved.RunID, Recipes: options.Recipes, EnumSets: options.EnumSets})
	if err != nil {
		return fmt.Errorf("recipe %q %s @%s: %w", options.Resolved.Name, stage, ref.Target(), err)
	}
	nested := options
	nested.Resolved = resolved
	return validateResolvedPlan(ctx, nested, append(slices.Clone(stack), key))
}

func validateCrossConfigReference(ctx context.Context, options Options, stage string, ref recipe.RecipeReferenceTarget, dynamicArgs bool, stack []string) error {
	target, err := configfile.ResolveCrossConfigReference(ctx, ref.Path, options.Resolved.ConfigPath, options.SourceDir, configfile.ResolveOptions{EvalDynamicVars: true})
	if err != nil {
		return fmt.Errorf("recipe %q %s @%s: %w", options.Resolved.Name, stage, ref.Target(), err)
	}
	key := recipeReferenceStackKey(target.Loaded.Path, ref.Name)
	if slices.Contains(stack, key) {
		cycle := append(slices.Clone(stack), key)
		return fmt.Errorf("recipe reference cycle: %s", joinReferenceStack(cycle))
	}
	rec, ok := target.Recipes[ref.Name]
	if !ok {
		return fmt.Errorf("recipe %q %s: unknown recipe reference: @%s", options.Resolved.Name, stage, ref.Target())
	}
	if dynamicArgs {
		return nil
	}
	resolved, err := recipe.ResolveWithOptions(ref.Name, rec, ref.Args, nil, target.Loaded.Config.Env, target.Loaded.Path, target.Profile, recipe.ResolveOptions{RunID: options.Resolved.RunID, Recipes: target.Recipes, EnumSets: target.Loaded.Config.EnumSets})
	if err != nil {
		return fmt.Errorf("recipe %q %s @%s: %w", options.Resolved.Name, stage, ref.Target(), err)
	}
	nested := options
	nested.Resolved = resolved
	nested.Recipes = target.Recipes
	nested.EnumSets = target.Loaded.Config.EnumSets
	nested.ConfigEnv = target.Loaded.Config.Env
	nested.SourceDir = filepath.Clean(target.Dir)
	return validateResolvedPlan(ctx, nested, append(slices.Clone(stack), key))
}

func joinReferenceStack(stack []string) string {
	out := slices.Clone(stack)
	for i, item := range out {
		out[i] = filepath.ToSlash(item)
	}
	return strings.Join(out, " -> ")
}

func expandedForEachCommand(resolved recipe.Resolved) recipe.Command {
	command := resolved.Recipe.ForEach
	if len(command) == 0 {
		return nil
	}
	if _, ok, _ := recipe.ValueBuiltinUsesFilesystem(command); ok {
		return command
	}
	return recipe.CommandWithRecipeReference(command, resolved.Recipe.Shell, resolved.Recipe.ShellPrelude)
}
