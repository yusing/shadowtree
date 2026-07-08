package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

const retrySleepCommand = "__shadowtree_retry_sleep"

func runScriptCommand(ctx context.Context, sandbox *sandboxWorkspace, dir string, env []string, command recipe.Command, stdin io.Reader, stdout, stderr io.Writer, options Options, stack []string) error {
	shell := recipe.ScriptShell(command)
	if shell == "" {
		shell = "sh"
	}
	if !scriptref.SupportedShell(shell) {
		return runExternalCommand(ctx, dir, env, recipe.ShellCommand(command), stdin, stdout, stderr)
	}
	body := recipe.ScriptBody(command)
	body, rewritten, err := rewriteRetryInvocations(shell, body)
	if err != nil {
		return err
	}
	if !rewritten && !strings.Contains(body, "@") {
		return runExternalCommand(ctx, dir, env, scriptShellCommand(command, shell, body), stdin, stdout, stderr)
	}
	file, refs, err := scriptref.Parse(shell, body)
	if err != nil {
		return err
	}
	references := scriptReferencePositions(refs)
	if !rewritten && len(references) == 0 {
		return runExternalCommand(ctx, dir, env, scriptShellCommand(command, shell, body), stdin, stdout, stderr)
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
			if args[0] == retrySleepCommand {
				return runRetrySleep(ctx, args)
			}
			if _, ok := references[handler.Pos]; !ok {
				return next(ctx, args)
			}
			runner := scriptCommandRunner{
				sandbox: sandbox,
				handler: handler,
				options: options,
				stack:   stack,
				env:     scriptEnvironList(handler.Env, baseEnv),
			}
			if _, ok := recipe.ParseRecipeReference(recipe.Command(args)); !ok || options.Recipes == nil {
				return next(ctx, args)
			}
			return runner.runRecipeReference(ctx, recipe.Command(args))
		}
	}
}

type scriptCommandRunner struct {
	sandbox *sandboxWorkspace
	handler interp.HandlerContext
	options Options
	stack   []string
	env     []string
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

func runRetrySleep(ctx context.Context, args []string) error {
	if len(args) != 2 {
		return errors.New("invalid @retry sleep")
	}
	ns, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil || ns < 0 {
		return errors.New("invalid @retry sleep")
	}
	timer := time.NewTimer(time.Duration(ns))
	select {
	case <-ctx.Done():
		timer.Stop()
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type retryOptions struct {
	count int
	delay time.Duration
}

func rewriteRetryInvocations(shell, body string) (string, bool, error) {
	if !strings.Contains(body, "@"+recipe.RetryCommandHelper) {
		return body, false, nil
	}
	parser, err := scriptref.Parser(shell)
	if err != nil {
		return "", false, err
	}
	file, err := parser.Parse(strings.NewReader(body), "shadowtree")
	if err != nil {
		return "", false, err
	}
	funcNames := shellFunctionNames(file)
	var retryFuncs []string
	var walkErr error
	syntax.Walk(file, func(node syntax.Node) bool {
		if walkErr != nil {
			return false
		}
		call, ok := node.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		opts, target, ok, err := retryCallInvocation(call)
		if err != nil {
			walkErr = err
			return false
		}
		if !ok {
			return true
		}
		name := retryFunctionName(call.Args[0].Pos(), len(retryFuncs)+1)
		if _, ok := funcNames[name]; ok {
			walkErr = fmt.Errorf("@retry generated helper %q conflicts with a shell function", name)
			return false
		}
		targetSource, err := shellCallSource(target)
		if err != nil {
			walkErr = err
			return false
		}
		retryFuncs = append(retryFuncs, shellRetryFunction(name, opts, targetSource))
		call.Args = []*syntax.Word{shellLiteralWord(name)}
		return true
	})
	if walkErr != nil {
		return "", false, walkErr
	}
	if len(retryFuncs) == 0 {
		return body, false, nil
	}
	var printed bytes.Buffer
	if err := syntax.NewPrinter().Print(&printed, file); err != nil {
		return "", false, err
	}
	return recipe.JoinShell(strings.Join(retryFuncs, "\n"), printed.String()), true, nil
}

func shellFunctionNames(file *syntax.File) map[string]struct{} {
	names := map[string]struct{}{}
	syntax.Walk(file, func(node syntax.Node) bool {
		fn, ok := node.(*syntax.FuncDecl)
		if !ok {
			return true
		}
		if fn.Name != nil {
			names[fn.Name.Value] = struct{}{}
		}
		for _, name := range fn.Names {
			names[name.Value] = struct{}{}
		}
		return true
	})
	return names
}

func retryFunctionName(pos syntax.Pos, index int) string {
	return fmt.Sprintf("__shadowtree_retry_%d_%d_%d", pos.Line(), pos.Col(), index)
}

func retryCallInvocation(call *syntax.CallExpr) (retryOptions, []*syntax.Word, bool, error) {
	first, err := retryLiteralWord(call.Args[0])
	if err != nil || first != "@"+recipe.RetryCommandHelper && !strings.HasPrefix(first, "@"+recipe.RetryCommandHelper+"[") {
		return retryOptions{}, nil, false, nil
	}
	var helper []string
	for _, word := range call.Args {
		value, err := retryLiteralWord(word)
		if err != nil {
			return retryOptions{}, nil, true, err
		}
		helper = append(helper, value)
		parsedHelper, targetStart, err := retryHelperInvocation(helper)
		if err != nil {
			continue
		}
		opts, err := parseRetryOptions(parsedHelper)
		if err != nil {
			return retryOptions{}, nil, true, err
		}
		if targetStart >= len(call.Args) {
			return retryOptions{}, nil, true, errors.New("@retry requires a command")
		}
		return opts, call.Args[targetStart:], true, nil
	}
	return retryOptions{}, nil, true, errors.New("invalid @retry syntax")
}

func retryInvocation(args []string) (recipe.Command, bool, error) {
	prefix := "@" + recipe.RetryCommandHelper
	if len(args) == 0 || args[0] != prefix && !strings.HasPrefix(args[0], prefix+"[") {
		return nil, false, nil
	}
	helper, targetStart, err := retryHelperInvocation(args)
	if err != nil {
		return nil, true, err
	}
	target := recipe.Command(args[targetStart:])
	if _, err := parseRetryOptions(helper); err != nil {
		return nil, true, err
	}
	if len(target) == 0 {
		return nil, true, errors.New("@retry requires a command")
	}
	return target, true, nil
}

func parseRetryOptions(helper []string) (retryOptions, error) {
	opts := retryOptions{count: 3, delay: time.Second}
	name, optionArgs := recipe.Invocation(helper)
	if name != recipe.RetryCommandHelper {
		return retryOptions{}, errors.New("invalid @retry syntax")
	}
	for _, option := range optionArgs {
		key, value, ok := strings.Cut(strings.TrimSpace(option), "=")
		if !ok {
			return retryOptions{}, errors.New("@retry options must be key=value")
		}
		switch key {
		case "count":
			count, err := strconv.Atoi(value)
			if err != nil || count <= 0 {
				return retryOptions{}, errors.New("@retry count must be a positive integer")
			}
			opts.count = count
		case "delay":
			delay, err := time.ParseDuration(value)
			if err != nil || delay < 0 {
				return retryOptions{}, errors.New("@retry delay must be a non-negative duration")
			}
			opts.delay = delay
		default:
			return retryOptions{}, errors.New("@retry unsupported option " + strconv.Quote(key))
		}
	}
	return opts, nil
}

func retryHelperInvocation(args []string) ([]string, int, error) {
	first := strings.TrimPrefix(args[0], "@")
	if first == recipe.RetryCommandHelper {
		return []string{first}, 1, nil
	}
	helper := []string{first}
	for i := 1; i < len(args); i++ {
		if strings.HasSuffix(helper[len(helper)-1], "]") {
			return helper, i, nil
		}
		helper = append(helper, args[i])
	}
	if strings.HasSuffix(helper[len(helper)-1], "]") {
		return helper, len(args), nil
	}
	return nil, 0, errors.New("invalid @retry syntax")
}

func retryLiteralWord(word *syntax.Word) (string, error) {
	var b strings.Builder
	for _, part := range word.Parts {
		switch part := part.(type) {
		case *syntax.Lit:
			b.WriteString(part.Value)
		case *syntax.SglQuoted:
			b.WriteString(part.Value)
		case *syntax.DblQuoted:
			for _, quoted := range part.Parts {
				lit, ok := quoted.(*syntax.Lit)
				if !ok {
					return "", errors.New("invalid @retry syntax")
				}
				b.WriteString(lit.Value)
			}
		default:
			return "", errors.New("invalid @retry syntax")
		}
	}
	return b.String(), nil
}

func shellCallSource(args []*syntax.Word) (string, error) {
	var b bytes.Buffer
	err := syntax.NewPrinter().Print(&b, &syntax.CallExpr{Args: args})
	return b.String(), err
}

func shellRetryFunction(name string, opts retryOptions, targetSource string) string {
	attemptVar := name + "_attempt"
	statusVar := name + "_status"
	sleepLine := ""
	if opts.delay > 0 {
		sleepLine = fmt.Sprintf("\t\t%s %d\n", retrySleepCommand, opts.delay.Nanoseconds())
	}
	var b strings.Builder
	fmt.Fprintf(&b, `%s() {
	%s=1
	while :; do
		if %s; then
			return 0
		else
			%s=$?
		fi
		if [ "$%s" -ge %d ]; then
			return "$%s"
		fi
%s		%s=$((%s + 1))
	done
}
`, name, attemptVar, targetSource, statusVar, attemptVar, opts.count, statusVar, sleepLine, attemptVar, attemptVar)
	return b.String()
}

func shellLiteralWord(value string) *syntax.Word {
	return &syntax.Word{Parts: []syntax.WordPart{&syntax.Lit{Value: value}}}
}

func scriptShellCommand(command recipe.Command, shell, body string) recipe.Command {
	if len(command) >= 4 && recipe.IsScriptCommand(command) {
		return append(recipe.Command{shell, "-c", body}, command[3:]...)
	}
	return recipe.Command{shell, "-c", body, "shadowtree"}
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
