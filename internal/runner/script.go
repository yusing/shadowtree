package runner

import (
	"context"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

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
	if !strings.Contains(body, "@") {
		return runExternalCommand(ctx, dir, env, recipe.ShellCommand(command), stdin, stdout, stderr)
	}
	file, refs, err := scriptref.Parse(shell, body)
	if err != nil {
		return err
	}
	references := scriptReferencePositions(refs)
	if len(references) == 0 {
		return runExternalCommand(ctx, dir, env, recipe.ShellCommand(command), stdin, stdout, stderr)
	}
	exported := map[string]string{}
	exportCommands := map[syntax.Pos]struct{}{}
	runner, err := interp.New(
		interp.Env(expand.ListEnviron(env...)),
		interp.Dir(dir),
		interp.Params(scriptParams(command)...),
		interp.StdIO(stdin, stdout, stderr),
		interp.CallHandler(exportCallHandler(exported, exportCommands)),
		interp.ExecHandlers(recipeReferenceExecHandler(references, exported, exportCommands, sandbox, options, stack, env)),
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

func exportCallHandler(exported map[string]string, exportCommands map[syntax.Pos]struct{}) interp.CallHandlerFunc {
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
		exportCommands[handler.Pos] = struct{}{}
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

func recipeReferenceExecHandler(references map[syntax.Pos]struct{}, exported map[string]string, exportCommands map[syntax.Pos]struct{}, sandbox *sandboxWorkspace, options Options, stack []string, baseEnv []string) func(interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			if len(args) == 0 {
				return next(ctx, args)
			}
			handler := interp.HandlerCtx(ctx)
			if _, ok := exportCommands[handler.Pos]; ok {
				delete(exportCommands, handler.Pos)
			} else {
				syncScriptExports(handler.Env, exported)
			}
			if err := applyScriptExports(handler.Env, exported); err != nil {
				return err
			}
			if _, ok := references[handler.Pos]; !ok {
				return next(ctx, args)
			}
			runner := scriptCommandRunner{
				next:    next,
				sandbox: sandbox,
				handler: handler,
				options: options,
				stack:   stack,
				env:     scriptEnvironList(handler.Env, baseEnv),
			}
			if opts, target, ok, err := retryInvocation(args); ok {
				if err != nil {
					return err
				}
				return runRetryInvocation(ctx, opts, target, runner)
			}
			if _, ok := recipe.ParseRecipeReference(recipe.Command(args)); !ok || options.Recipes == nil {
				return next(ctx, args)
			}
			return runner.runRecipeReference(ctx, recipe.Command(args))
		}
	}
}

type scriptCommandRunner struct {
	next    interp.ExecHandlerFunc
	sandbox *sandboxWorkspace
	handler interp.HandlerContext
	options Options
	stack   []string
	env     []string
}

func (runner scriptCommandRunner) run(ctx context.Context, target recipe.Command) error {
	if _, ok := recipe.ParseRecipeReference(target); ok && runner.options.Recipes != nil {
		return runner.runRecipeReference(ctx, target)
	}
	return runner.next(ctx, target)
}

func (runner scriptCommandRunner) runRecipeReference(ctx context.Context, target recipe.Command) error {
	err := runRecipeReference(ctx, runner.sandbox, runner.handler.Dir, runner.env, target, runner.handler.Stdin, runner.handler.Stdout, runner.handler.Stderr, runner.options, runner.stack)
	return scriptCommandError(err)
}

func scriptCommandError(err error) error {
	if err == nil {
		return nil
	}
	var exitErr ExitError
	if errors.As(err, &exitErr) {
		return interp.ExitStatus(uint8(exitErr.Code))
	}
	return err
}

type retryOptions struct {
	count int
	delay time.Duration
}

func retryInvocation(args []string) (retryOptions, recipe.Command, bool, error) {
	prefix := "@" + recipe.RetryCommandHelper
	if len(args) == 0 || args[0] != prefix && !strings.HasPrefix(args[0], prefix+"[") {
		return retryOptions{}, nil, false, nil
	}
	opts := retryOptions{count: 3, delay: time.Second}
	helper, targetStart, err := retryHelperInvocation(args)
	if err != nil {
		return retryOptions{}, nil, true, err
	}
	target := recipe.Command(args[targetStart:])
	name, optionArgs := recipe.Invocation(helper)
	if name != recipe.RetryCommandHelper {
		return retryOptions{}, nil, true, errors.New("invalid @retry syntax")
	}
	for _, option := range optionArgs {
		key, value, ok := strings.Cut(strings.TrimSpace(option), "=")
		if !ok {
			return retryOptions{}, nil, true, errors.New("@retry options must be key=value")
		}
		switch key {
		case "count":
			count, err := strconv.Atoi(value)
			if err != nil || count <= 0 {
				return retryOptions{}, nil, true, errors.New("@retry count must be a positive integer")
			}
			opts.count = count
		case "delay":
			delay, err := time.ParseDuration(value)
			if err != nil || delay < 0 {
				return retryOptions{}, nil, true, errors.New("@retry delay must be a non-negative duration")
			}
			opts.delay = delay
		default:
			return retryOptions{}, nil, true, errors.New("@retry unsupported option " + strconv.Quote(key))
		}
	}
	if len(target) == 0 {
		return opts, nil, true, errors.New("@retry requires a command")
	}
	return opts, target, true, nil
}

func retryHelperInvocation(args []string) ([]string, int, error) {
	first := strings.TrimPrefix(args[0], "@")
	if first == recipe.RetryCommandHelper {
		return []string{first}, 1, nil
	}
	helper := []string{first}
	for i := 1; i < len(args); i++ {
		if strings.HasSuffix(strings.Join(helper, " "), "]") {
			return helper, i, nil
		}
		helper = append(helper, args[i])
	}
	if strings.HasSuffix(strings.Join(helper, " "), "]") {
		return helper, len(args), nil
	}
	return nil, 0, errors.New("invalid @retry syntax")
}

func runRetryInvocation(ctx context.Context, opts retryOptions, target recipe.Command, runner scriptCommandRunner) error {
	var err error
	for attempt := 1; attempt <= opts.count; attempt++ {
		err = runner.run(ctx, target)
		if err == nil || ctx.Err() != nil {
			return err
		}
		if attempt == opts.count {
			break
		}
		if opts.delay > 0 {
			timer := time.NewTimer(opts.delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return scriptCommandError(err)
}

func syncScriptExports(env expand.Environ, exported map[string]string) {
	for name := range exported {
		vr := env.Get(name)
		if !vr.IsSet() || vr.Kind != expand.String {
			delete(exported, name)
			continue
		}
		exported[name] = vr.Str
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

func scriptEnvironList(env expand.Environ, baseEnv []string) []string {
	values := envListMap(baseEnv)
	baseNames := make(map[string]struct{}, len(values))
	for name := range values {
		baseNames[name] = struct{}{}
		vr := env.Get(name)
		if !vr.IsSet() || !vr.Exported || vr.Kind != expand.String {
			delete(values, name)
			continue
		}
		values[name] = vr.Str
	}
	env.Each(func(name string, vr expand.Variable) bool {
		if _, ok := baseNames[name]; ok {
			return true
		}
		if vr.IsSet() && vr.Exported && vr.Kind == expand.String {
			values[name] = vr.Str
		}
		return true
	})
	return envMapList(values)
}
