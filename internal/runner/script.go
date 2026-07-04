package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/yusing/shadowtree/internal/recipe"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

func runScriptCommand(ctx context.Context, sandbox *sandboxWorkspace, dir string, env []string, command recipe.Command, stdin io.Reader, stdout, stderr io.Writer, options Options, stack []string) error {
	shell := recipe.ScriptShell(command)
	if shell == "" {
		shell = "sh"
	}
	if !scriptShellSupported(shell) {
		return runExternalCommand(ctx, dir, env, recipe.ShellCommand(command), stdin, stdout, stderr)
	}
	body := recipe.ScriptBody(command)
	file, references, err := parseScriptReferences(shell, body)
	if err != nil {
		return err
	}
	if len(references) == 0 {
		return runExternalCommand(ctx, dir, env, recipe.ShellCommand(command), stdin, stdout, stderr)
	}
	runner, err := interp.New(
		interp.Env(expand.ListEnviron(env...)),
		interp.Dir(dir),
		interp.Params(scriptParams(command)...),
		interp.StdIO(stdin, stdout, stderr),
		interp.ExecHandlers(recipeReferenceExecHandler(references, sandbox, options, stack)),
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

func parseScriptReferences(shell, body string) (*syntax.File, map[syntax.Pos]struct{}, error) {
	parser, err := scriptParser(shell)
	if err != nil {
		return nil, nil, err
	}
	file, err := parser.Parse(strings.NewReader(body), "shadowtree")
	if err != nil {
		return nil, nil, err
	}
	references := map[syntax.Pos]struct{}{}
	syntax.Walk(file, func(node syntax.Node) bool {
		call, ok := node.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		if strings.HasPrefix(call.Args[0].Lit(), "@") {
			references[call.Pos()] = struct{}{}
		}
		return true
	})
	return file, references, nil
}

func scriptParser(shell string) (*syntax.Parser, error) {
	switch shell {
	case "", "sh":
		return syntax.NewParser(syntax.Variant(syntax.LangPOSIX)), nil
	case "bash":
		return syntax.NewParser(syntax.Variant(syntax.LangBash)), nil
	default:
		return nil, fmt.Errorf("script recipe references require shell sh or bash, got %q", shell)
	}
}

func scriptShellSupported(shell string) bool {
	return shell == "" || shell == "sh" || shell == "bash"
}

func scriptParams(command recipe.Command) []string {
	return slices.Concat([]string{"--"}, command[4:])
}

func recipeReferenceExecHandler(references map[syntax.Pos]struct{}, sandbox *sandboxWorkspace, options Options, stack []string) func(interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			if len(args) == 0 {
				return next(ctx, args)
			}
			handler := interp.HandlerCtx(ctx)
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
