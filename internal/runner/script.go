package runner

import (
	"context"
	"errors"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/yusing/shadowtree/internal/recipe"
	"github.com/yusing/shadowtree/internal/scriptref"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

func runScriptCommand(ctx context.Context, sandbox *sandboxWorkspace, dir string, env []string, command recipe.Command, stdin io.Reader, stdout, stderr io.Writer, options Options, stack []string) error {
	shell := recipe.ScriptShell(command)
	if shell == "" {
		shell = "sh"
	}
	if !scriptref.SupportedShell(shell) {
		return runExternalCommand(ctx, dir, env, recipe.ShellCommand(command), stdin, stdout, stderr)
	}
	body := recipe.ScriptBody(command)
	file, refs, err := scriptref.Parse(shell, body)
	if err != nil {
		return err
	}
	references := scriptReferencePositions(refs)
	if len(references) == 0 {
		return runExternalCommand(ctx, dir, env, recipe.ShellCommand(command), stdin, stdout, stderr)
	}
	exported := map[string]string{}
	runner, err := interp.New(
		interp.Env(expand.ListEnviron(env...)),
		interp.Dir(dir),
		interp.Params(scriptParams(command)...),
		interp.StdIO(stdin, stdout, stderr),
		interp.CallHandler(exportCallHandler(exported)),
		interp.ExecHandlers(recipeReferenceExecHandler(references, exported, sandbox, options, stack)),
	)
	if err != nil {
		return err
	}
	if err := runner.Run(ctx, file); err != nil {
		var status interp.ExitStatus
		if errors.As(err, &status) {
			return ExitError{Code: int(status)}
		}
		return err
	}
	return nil
}

func exportCallHandler(exported map[string]string) interp.CallHandlerFunc {
	return func(ctx context.Context, args []string) ([]string, error) {
		if len(args) < 2 || args[0] != "export" {
			return args, nil
		}
		handler := interp.HandlerCtx(ctx)
		for _, arg := range args[1:] {
			if arg == "" || arg[0] == '-' {
				return args, nil
			}
			name, value, hasValue := strings.Cut(arg, "=")
			if !syntax.ValidName(name) {
				return args, nil
			}
			if hasValue {
				exported[name] = value
				continue
			}
			current := handler.Env.Get(name)
			if current.IsSet() && current.Kind == expand.String {
				exported[name] = current.Str
			}
		}
		return []string{":"}, nil
	}
}

func scriptReferencePositions(refs []scriptref.Reference) map[syntax.Pos]struct{} {
	references := map[syntax.Pos]struct{}{}
	for _, ref := range refs {
		references[ref.CommandPos] = struct{}{}
	}
	return references
}

func scriptParams(command recipe.Command) []string {
	return slices.Concat([]string{"--"}, command[4:])
}

func recipeReferenceExecHandler(references map[syntax.Pos]struct{}, exported map[string]string, sandbox *sandboxWorkspace, options Options, stack []string) func(interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			if len(args) == 0 {
				return next(ctx, args)
			}
			handler := interp.HandlerCtx(ctx)
			if err := applyScriptExports(handler.Env, exported); err != nil {
				return err
			}
			if _, ok := references[handler.Pos]; !ok {
				return next(ctx, args)
			}
			if _, ok := recipe.ParseRecipeReference(recipe.Command(args)); !ok || options.Recipes == nil {
				return next(ctx, args)
			}
			env := environList(handler.Env)
			err := runRecipeReference(ctx, sandbox, handler.Dir, env, recipe.Command(args), handler.Stdin, handler.Stdout, handler.Stderr, options, stack)
			if err == nil {
				return nil
			}
			var exitErr ExitError
			if errors.As(err, &exitErr) {
				return interp.ExitStatus(uint8(exitErr.Code))
			}
			return err
		}
	}
}

func applyScriptExports(env expand.Environ, exported map[string]string) error {
	if len(exported) == 0 {
		return nil
	}
	writeEnv, ok := env.(expand.WriteEnviron)
	if !ok {
		return nil
	}
	for name, value := range exported {
		if err := writeEnv.Set(name, expand.Variable{
			Set:      true,
			Exported: true,
			Kind:     expand.String,
			Str:      value,
		}); err != nil {
			return err
		}
	}
	return nil
}

func environList(env expand.Environ) []string {
	values := map[string]string{}
	env.Each(func(name string, vr expand.Variable) bool {
		if vr.IsSet() && vr.Exported && vr.Kind == expand.String {
			values[name] = vr.Str
		}
		return true
	})
	out := make([]string, 0, len(values))
	for _, name := range slices.Sorted(maps.Keys(values)) {
		out = append(out, name+"="+values[name])
	}
	return out
}
