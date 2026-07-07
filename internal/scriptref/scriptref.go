package scriptref

import (
	"fmt"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// Position is a zero-based byte position inside a parsed shell script body.
type Position struct {
	Line int
	Col  int
}

// Reference describes a literal command-position @recipe shell call.
type Reference struct {
	// CommandPos is the shell AST command position used by the interpreter.
	CommandPos syntax.Pos
	Start      Position // start of the @recipe word
	End        Position // end of the full shell command
	TargetEnd  Position // end of the @recipe word
	Value      string   // literal @recipe word
	Args       []Argument
}

// Argument describes a shell word passed to a recipe reference.
type Argument struct {
	Start   Position
	End     Position
	Value   string
	Dynamic bool
}

// SupportedShell reports whether shell can be parsed for recipe references.
func SupportedShell(shell string) bool {
	return shell == "" || shell == "sh" || shell == "bash"
}

// Parse parses body and returns its AST plus literal command-position recipe references.
func Parse(shell, body string) (*syntax.File, []Reference, error) {
	parser, err := Parser(shell)
	if err != nil {
		return nil, nil, err
	}
	file, err := parser.Parse(strings.NewReader(body), "shadowtree")
	if err != nil {
		return nil, nil, err
	}
	return file, References(file), nil
}

// Parser returns a shell parser configured for Shadowtree script references.
func Parser(shell string) (*syntax.Parser, error) {
	switch shell {
	case "", "sh":
		return syntax.NewParser(syntax.Variant(syntax.LangPOSIX)), nil
	case "bash":
		return syntax.NewParser(syntax.Variant(syntax.LangBash)), nil
	default:
		return nil, fmt.Errorf("script recipe references require shell sh or bash, got %q", shell)
	}
}

// References returns literal command-position @recipe calls from file.
func References(file *syntax.File) []Reference {
	var references []Reference
	syntax.Walk(file, func(node syntax.Node) bool {
		call, ok := node.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		target := call.Args[0]
		value := target.Lit()
		if !strings.HasPrefix(value, "@") {
			return true
		}
		ref := Reference{
			CommandPos: call.Pos(),
			Start:      position(target.Pos()),
			End:        position(call.End()),
			TargetEnd:  position(target.End()),
			Value:      value,
		}
		for _, arg := range call.Args[1:] {
			ref.Args = append(ref.Args, Argument{
				Start:   position(arg.Pos()),
				End:     position(arg.End()),
				Value:   arg.Lit(),
				Dynamic: wordHasExpansion(arg),
			})
		}
		references = append(references, ref)
		return true
	})
	return references
}

func wordHasExpansion(word *syntax.Word) bool {
	for _, part := range word.Parts {
		if wordPartHasExpansion(part) {
			return true
		}
	}
	return false
}

func wordPartHasExpansion(part syntax.WordPart) bool {
	switch part := part.(type) {
	case *syntax.ParamExp, *syntax.CmdSubst, *syntax.ArithmExp, *syntax.ProcSubst:
		return true
	case *syntax.DblQuoted:
		for _, nested := range part.Parts {
			if wordPartHasExpansion(nested) {
				return true
			}
		}
	}
	return false
}

func position(pos syntax.Pos) Position {
	return Position{
		Line: max(int(pos.Line())-1, 0),
		Col:  max(int(pos.Col())-1, 0),
	}
}
