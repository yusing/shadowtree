package shadowtreelsp

import (
	"slices"
	"strings"
	"unicode/utf16"
)

type documentAnalysis struct {
	Lines        []string
	Recipes      []string
	GlobalVars   []string
	RecipeVars   map[string][]string
	Arguments    map[string][]string
	CurrentTable string
}

type completion struct {
	Label      string
	InsertText string
	Kind       int
	Detail     string
	Quote      bool
}

const (
	completionKindFunction = 3
	completionKindField    = 5
	completionKindVariable = 6
	completionKindValue    = 12
	completionKindKeyword  = 14
)

var topKeys = []completion{
	{Label: "profile", InsertText: `profile = "go"`, Kind: completionKindKeyword, Detail: "Shadowtree profile"},
	{Label: "shell", InsertText: `shell = "sh"`, Kind: completionKindKeyword, Detail: "Shell for script commands"},
	{Label: "shell_prelude", InsertText: "shell_prelude = '''\n\n'''", Kind: completionKindKeyword, Detail: "Shared shell code"},
	{Label: "sync_out", InsertText: "sync_out = []", Kind: completionKindKeyword, Detail: "Global sync-out paths"},
}

var recipeKeys = []completion{
	{Label: "help", InsertText: `help = ""`, Kind: completionKindKeyword, Detail: "Recipe help text"},
	{Label: "shell", InsertText: `shell = "sh"`, Kind: completionKindKeyword, Detail: "Recipe shell"},
	{Label: "shell_prelude", InsertText: "shell_prelude = '''\n\n'''", Kind: completionKindKeyword, Detail: "Recipe shell prelude"},
	{Label: "sandboxed", InsertText: "sandboxed = true", Kind: completionKindKeyword, Detail: "Run in disposable workspace"},
	{Label: "cmd", InsertText: `cmd = [""]`, Kind: completionKindKeyword, Detail: "Main command"},
	{Label: "args", InsertText: "args = []", Kind: completionKindKeyword, Detail: "Fixed args"},
	{Label: "default_args", InsertText: "default_args = []", Kind: completionKindKeyword, Detail: "Default CLI args"},
	{Label: "pre", InsertText: "pre = []", Kind: completionKindKeyword, Detail: "Commands before main"},
	{Label: "post", InsertText: "post = []", Kind: completionKindKeyword, Detail: "Commands after main"},
	{Label: "sync_out", InsertText: "sync_out = []", Kind: completionKindKeyword, Detail: "Recipe sync-out paths"},
}

var argumentKeys = []completion{
	{Label: "help", InsertText: `help = ""`, Kind: completionKindKeyword, Detail: "Argument help text"},
	{Label: "type", InsertText: `type = "string"`, Kind: completionKindKeyword, Detail: "Argument type"},
	{Label: "position", InsertText: "position = 1", Kind: completionKindKeyword, Detail: "1-based positional index"},
	{Label: "required", InsertText: "required = false", Kind: completionKindKeyword, Detail: "Whether the argument is required"},
	{Label: "default", InsertText: `default = ""`, Kind: completionKindKeyword, Detail: "Default value"},
	{Label: "values", InsertText: "values = '''\n\n'''", Kind: completionKindKeyword, Detail: "Dynamic completion command"},
}

var shellValues = []completion{
	{Label: "sh", InsertText: "sh", Kind: completionKindValue, Detail: "POSIX shell", Quote: true},
	{Label: "bash", InsertText: "bash", Kind: completionKindValue, Detail: "Bash", Quote: true},
	{Label: "fish", InsertText: "fish", Kind: completionKindValue, Detail: "Fish", Quote: true},
}

var argumentTypeValues = []completion{
	{Label: "string", InsertText: "string", Kind: completionKindValue, Detail: "String argument", Quote: true},
	{Label: "int", InsertText: "int", Kind: completionKindValue, Detail: "Integer argument", Quote: true},
	{Label: "float", InsertText: "float", Kind: completionKindValue, Detail: "Float argument", Quote: true},
	{Label: "bool", InsertText: "bool", Kind: completionKindValue, Detail: "Boolean argument", Quote: true},
}

var boolValues = []completion{
	{Label: "true", InsertText: "true", Kind: completionKindValue, Detail: "Boolean true"},
	{Label: "false", InsertText: "false", Kind: completionKindValue, Detail: "Boolean false"},
}

func analyzeDocument(text string, line int) documentAnalysis {
	lines := strings.Split(text, "\n")
	analysis := documentAnalysis{
		Lines:      lines,
		RecipeVars: map[string][]string{},
		Arguments:  map[string][]string{},
	}
	table := ""
	for i, raw := range lines {
		if parsed, ok := completeTableHeader(raw); ok {
			table = parsed
			if strings.HasPrefix(parsed, "recipes.") {
				parts := strings.Split(parsed, ".")
				if len(parts) >= 2 {
					analysis.Recipes = appendUnique(analysis.Recipes, parts[1])
				}
				if len(parts) >= 4 && parts[2] == "arguments" {
					analysis.Arguments[parts[1]] = appendUnique(analysis.Arguments[parts[1]], parts[3])
				}
			}
		}
		if key, ok := pairKey(raw); ok {
			switch {
			case table == "vars" || table == "var_commands":
				analysis.GlobalVars = appendUnique(analysis.GlobalVars, key)
			case strings.HasPrefix(table, "recipes."):
				parts := strings.Split(table, ".")
				if len(parts) == 3 && parts[2] == "vars" {
					analysis.RecipeVars[parts[1]] = appendUnique(analysis.RecipeVars[parts[1]], key)
				}
			}
		}
		if i == line {
			analysis.CurrentTable = table
		}
	}
	slices.Sort(analysis.Recipes)
	slices.Sort(analysis.GlobalVars)
	for name := range analysis.RecipeVars {
		slices.Sort(analysis.RecipeVars[name])
	}
	for name := range analysis.Arguments {
		slices.Sort(analysis.Arguments[name])
	}
	return analysis
}

func completionsAt(text string, pos lspPosition) []completion {
	analysis := analyzeDocument(text, pos.Line)
	line := lineAt(analysis.Lines, pos.Line)
	prefix := linePrefix(line, pos.Character)
	if tablePrefix, ok := openTablePrefix(prefix); ok {
		return tableCompletions(analysis, tablePrefix)
	}
	if variablePrefix, ok := placeholderPrefix(prefix); ok {
		return placeholderCompletions(analysis, currentRecipe(analysis.CurrentTable), variablePrefix)
	}
	if key, ok := keyBeforeValue(prefix); ok {
		return valueCompletions(key)
	}
	return keyCompletions(analysis.CurrentTable)
}

func tableCompletions(analysis documentAnalysis, prefix string) []completion {
	switch {
	case prefix == "":
		return []completion{
			{Label: "env", InsertText: "env]", Kind: completionKindField, Detail: "Environment variables"},
			{Label: "vars", InsertText: "vars]", Kind: completionKindVariable, Detail: "Shared placeholders"},
			{Label: "var_commands", InsertText: "var_commands]", Kind: completionKindFunction, Detail: "Dynamic placeholders"},
			{Label: "recipes", InsertText: "recipes.", Kind: completionKindField, Detail: "Recipe table"},
		}
	case prefix == "recipes" || prefix == "recipes.":
		items := []completion{
			{Label: "<name>", InsertText: "", Kind: completionKindField, Detail: "New recipe name"},
		}
		for _, recipe := range analysis.Recipes {
			items = append(items, completion{
				Label:      recipe,
				InsertText: recipe + "]",
				Kind:       completionKindField,
				Detail:     "Existing recipe",
			})
		}
		return items
	}
	recipe, rest, ok := strings.Cut(strings.TrimPrefix(prefix, "recipes."), ".")
	if !ok || recipe == "" {
		return nil
	}
	switch {
	case rest == "":
		return recipeSubtableCompletions()
	case rest == "arguments" || rest == "arguments.":
		items := []completion{
			{Label: "<name>", InsertText: "", Kind: completionKindField, Detail: "New argument name"},
		}
		for _, arg := range analysis.Arguments[recipe] {
			items = append(items, completion{
				Label:      arg,
				InsertText: arg + "]",
				Kind:       completionKindVariable,
				Detail:     "Existing argument",
			})
		}
		return items
	}
	return recipeSubtableCompletions()
}

func recipeSubtableCompletions() []completion {
	return []completion{
		{Label: "vars", InsertText: "vars]", Kind: completionKindVariable, Detail: "Recipe placeholders"},
		{Label: "env", InsertText: "env]", Kind: completionKindField, Detail: "Recipe environment"},
		{Label: "arguments", InsertText: "arguments.", Kind: completionKindField, Detail: "Recipe argument"},
	}
}

func keyCompletions(table string) []completion {
	switch {
	case table == "":
		return topKeys
	case strings.HasPrefix(table, "recipes."):
		parts := strings.Split(table, ".")
		if len(parts) == 2 {
			return recipeKeys
		}
		if len(parts) == 4 && parts[2] == "arguments" {
			return argumentKeys
		}
	}
	return nil
}

func valueCompletions(key string) []completion {
	switch key {
	case "profile":
		return []completion{{Label: "go", InsertText: "go", Kind: completionKindValue, Detail: "Go profile", Quote: true}}
	case "shell":
		return shellValues
	case "type":
		return argumentTypeValues
	case "sandboxed", "required":
		return boolValues
	default:
		return nil
	}
}

func placeholderCompletions(analysis documentAnalysis, recipe, prefix string) []completion {
	var names []string
	names = append(names, analysis.GlobalVars...)
	names = append(names, analysis.RecipeVars[recipe]...)
	names = append(names, analysis.Arguments[recipe]...)
	names = uniqueSorted(names)
	var items []completion
	for _, name := range names {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		insertText := name
		if !strings.HasSuffix(prefix, "}") {
			insertText += "}"
		}
		items = append(items, completion{
			Label:      "{" + name + "}",
			InsertText: insertText,
			Kind:       completionKindVariable,
			Detail:     "Shadowtree placeholder",
		})
	}
	return items
}

func semanticTokens(text string) []uint32 {
	lines := strings.Split(text, "\n")
	var tokens []semanticToken
	table := ""
	shells := shellSettings(lines)
	for lineNo, raw := range lines {
		if parsed, ok := completeTableHeader(raw); ok {
			table = parsed
		}
		key, ok := pairKey(raw)
		if ok && (table == "vars" || table == "var_commands" || strings.HasSuffix(table, ".vars")) {
			col := strings.Index(raw, key)
			tokens = append(tokens, semanticToken{Line: lineNo, Start: col, Length: len(key), Type: semanticTokenVariable})
		}
		for _, span := range placeholderSpans(raw) {
			tokens = append(tokens, semanticToken{Line: lineNo, Start: span.Start, Length: span.Length, Type: semanticTokenVariable})
		}
	}
	for _, region := range scriptRegions(lines, shells) {
		tokens = append(tokens, shellSemanticTokens(lines, region)...)
	}
	slices.SortFunc(tokens, func(a, b semanticToken) int {
		if a.Line != b.Line {
			return a.Line - b.Line
		}
		return a.Start - b.Start
	})
	return encodeSemanticTokens(lines, tokens)
}

type scriptRegion struct {
	Shell     string
	StartLine int
	StartCol  int
	EndLine   int
	EndCol    int
}

func shellSettings(lines []string) map[string]string {
	shells := map[string]string{"": "sh"}
	table := ""
	for _, raw := range lines {
		if parsed, ok := completeTableHeader(raw); ok {
			table = parsed
			if _, ok := shells[table]; !ok {
				shells[table] = shells[""]
			}
			continue
		}
		key, ok := pairKey(raw)
		if !ok || key != "shell" {
			continue
		}
		if value, ok := pairStringValue(raw); ok && supportedShell(value) {
			shells[table] = value
		}
	}
	return shells
}

func scriptRegions(lines []string, shells map[string]string) []scriptRegion {
	var regions []scriptRegion
	table := ""
	for lineNo := 0; lineNo < len(lines); lineNo++ {
		raw := lines[lineNo]
		if parsed, ok := completeTableHeader(raw); ok {
			table = parsed
			continue
		}
		key, ok := pairKey(raw)
		if !ok || !scriptKey(key) {
			continue
		}
		start, quote, ok := stringStart(raw)
		if !ok {
			continue
		}
		shell := shellForTable(shells, table)
		if quote.Triple {
			bodyStart := start + len(quote.Delimiter)
			if end := strings.Index(raw[bodyStart:], quote.Delimiter); end >= 0 {
				regions = append(regions, scriptRegion{
					Shell: shell, StartLine: lineNo, StartCol: bodyStart,
					EndLine: lineNo, EndCol: bodyStart + end,
				})
				continue
			}
			endLine, endCol := findTripleStringEnd(lines, lineNo+1, quote.Delimiter)
			regions = append(regions, scriptRegion{
				Shell: shell, StartLine: lineNo, StartCol: bodyStart,
				EndLine: endLine, EndCol: endCol,
			})
			lineNo = endLine
			continue
		}
		bodyStart := start + 1
		bodyEnd := findStringEnd(raw, bodyStart, quote.Delimiter[0])
		regions = append(regions, scriptRegion{
			Shell: shell, StartLine: lineNo, StartCol: bodyStart,
			EndLine: lineNo, EndCol: bodyEnd,
		})
	}
	return regions
}

type quoteInfo struct {
	Delimiter string
	Triple    bool
}

func stringStart(line string) (int, quoteInfo, bool) {
	_, value, ok := strings.Cut(line, "=")
	if !ok {
		return 0, quoteInfo{}, false
	}
	base := len(line) - len(value)
	for base < len(line) && (line[base] == ' ' || line[base] == '\t') {
		base++
	}
	switch {
	case strings.HasPrefix(line[base:], `'''`):
		return base, quoteInfo{Delimiter: `'''`, Triple: true}, true
	case strings.HasPrefix(line[base:], `"""`):
		return base, quoteInfo{Delimiter: `"""`, Triple: true}, true
	case base < len(line) && (line[base] == '\'' || line[base] == '"'):
		return base, quoteInfo{Delimiter: line[base : base+1]}, true
	default:
		return 0, quoteInfo{}, false
	}
}

func findTripleStringEnd(lines []string, startLine int, delimiter string) (int, int) {
	for lineNo := startLine; lineNo < len(lines); lineNo++ {
		if col := strings.Index(lines[lineNo], delimiter); col >= 0 {
			return lineNo, col
		}
	}
	lastLine := len(lines) - 1
	if lastLine < 0 {
		return 0, 0
	}
	return lastLine, len(lines[lastLine])
}

func findStringEnd(line string, start int, quote byte) int {
	for col := start; col < len(line); col++ {
		if quote == '"' && line[col] == '\\' {
			col++
			continue
		}
		if line[col] == quote {
			return col
		}
	}
	return len(line)
}

func shellForTable(shells map[string]string, table string) string {
	if shell, ok := shells[table]; ok && shell != "" {
		return shell
	}
	if strings.HasPrefix(table, "recipes.") {
		recipe := "recipes." + currentRecipe(table)
		if shell, ok := shells[recipe]; ok && shell != "" {
			return shell
		}
	}
	return shells[""]
}

func pairStringValue(line string) (string, bool) {
	start, quote, ok := stringStart(line)
	if !ok || quote.Triple {
		return "", false
	}
	end := findStringEnd(line, start+1, quote.Delimiter[0])
	if end <= start {
		return "", false
	}
	return line[start+1 : end], true
}

func supportedShell(shell string) bool {
	return shell == "sh" || shell == "bash" || shell == "fish"
}

func scriptKey(key string) bool {
	return key == "cmd" || key == "shell_prelude" || key == "values"
}

func shellSemanticTokens(lines []string, region scriptRegion) []semanticToken {
	var tokens []semanticToken
	for lineNo := region.StartLine; lineNo <= region.EndLine && lineNo < len(lines); lineNo++ {
		line := lines[lineNo]
		start, end := 0, len(line)
		if lineNo == region.StartLine {
			start = region.StartCol
		}
		if lineNo == region.EndLine {
			end = region.EndCol
		}
		tokens = append(tokens, shellLineTokens(line, lineNo, start, end, region.Shell)...)
	}
	return tokens
}

func shellLineTokens(line string, lineNo, start, end int, shell string) []semanticToken {
	var tokens []semanticToken
	commandPosition := true
	fishExpectFunctionName := false
	fishInFunctionDeclaration := false
	fishExpectFunctionArgs := false
	fishExpectSetName := false
	fishExpectStatusSubcommand := false
	for col := start; col < end; {
		ch := line[col]
		if ch == '#' {
			tokens = append(tokens, semanticToken{Line: lineNo, Start: col, Length: end - col, Type: semanticTokenComment})
			break
		}
		if isShellSpace(ch) {
			col++
			continue
		}
		if ch == '\'' {
			col = skipShellString(line, col, end, ch)
			commandPosition = false
			continue
		}
		if ch == '"' {
			next, stringTokens := doubleQuotedShellTokens(line, lineNo, col, end, shell)
			tokens = append(tokens, stringTokens...)
			col = next
			commandPosition = false
			continue
		}
		if ch == '$' {
			if token, ok := shellVariableToken(line, lineNo, col, end, shell); ok {
				tokens = append(tokens, token)
				col += token.Length
				commandPosition = false
				continue
			}
		}
		if isShellOperator(ch) {
			tokens = append(tokens, semanticToken{Line: lineNo, Start: col, Length: 1, Type: semanticTokenOperator})
			commandPosition = true
			col++
			continue
		}
		if isShellWordStart(ch) {
			wordStart := col
			for col < end && isShellWordPart(line[col]) {
				col++
			}
			word := line[wordStart:col]
			switch {
			case shell == "fish" && fishExpectFunctionName:
				tokens = append(tokens, semanticToken{Line: lineNo, Start: wordStart, Length: len(word), Type: semanticTokenFunction})
				fishExpectFunctionName = false
				fishInFunctionDeclaration = true
				commandPosition = false
			case shell == "fish" && fishExpectFunctionArgs && !strings.HasPrefix(word, "-"):
				tokens = append(tokens, semanticToken{Line: lineNo, Start: wordStart, Length: len(word), Type: semanticTokenVariable})
				commandPosition = false
			case shell == "fish" && fishExpectSetName && !strings.HasPrefix(word, "-"):
				tokens = append(tokens, semanticToken{Line: lineNo, Start: wordStart, Length: len(word), Type: semanticTokenVariable})
				fishExpectSetName = false
				commandPosition = false
			case shell == "fish" && fishExpectStatusSubcommand && !strings.HasPrefix(word, "-"):
				tokens = append(tokens, semanticToken{Line: lineNo, Start: wordStart, Length: len(word), Type: semanticTokenParameter})
				fishExpectStatusSubcommand = false
				commandPosition = false
			case shellKeyword(shell, word):
				tokens = append(tokens, semanticToken{Line: lineNo, Start: wordStart, Length: len(word), Type: semanticTokenKeyword})
				if shell == "fish" {
					switch word {
					case "function":
						fishExpectFunctionName = true
					case "set":
						fishExpectSetName = true
					}
				}
				commandPosition = commandContinuesAfterKeyword(shell, word)
			case strings.HasPrefix(word, "-"):
				tokens = append(tokens, semanticToken{Line: lineNo, Start: wordStart, Length: len(word), Type: semanticTokenParameter})
				if shell == "fish" && fishInFunctionDeclaration && (word == "-a" || word == "--argument-names") {
					fishExpectFunctionArgs = true
				}
				commandPosition = false
			case commandPosition:
				tokens = append(tokens, semanticToken{Line: lineNo, Start: wordStart, Length: len(word), Type: semanticTokenFunction})
				if shell == "fish" && word == "status" {
					fishExpectStatusSubcommand = true
				}
				commandPosition = false
			default:
				commandPosition = false
			}
			continue
		}
		col++
	}
	return tokens
}

func shellVariableToken(line string, lineNo, start, end int, shell string) (semanticToken, bool) {
	if start+1 >= end {
		return semanticToken{}, false
	}
	if line[start+1] == '{' {
		close := start + 2
		for close < end && line[close] != '}' {
			close++
		}
		if close < end {
			return semanticToken{Line: lineNo, Start: start, Length: close - start + 1, Type: semanticTokenVariable}, true
		}
	}
	if shell == "fish" && isIdentPart(line[start+1]) {
		col := start + 2
		for col < end && isIdentPart(line[col]) {
			col++
		}
		return semanticToken{Line: lineNo, Start: start, Length: col - start, Type: semanticTokenVariable}, true
	}
	if strings.ContainsRune("0123456789@*#?$!-", rune(line[start+1])) {
		return semanticToken{Line: lineNo, Start: start, Length: 2, Type: semanticTokenVariable}, true
	}
	if !isIdentStart(line[start+1]) {
		return semanticToken{}, false
	}
	col := start + 2
	for col < end && isIdentPart(line[col]) {
		col++
	}
	return semanticToken{Line: lineNo, Start: start, Length: col - start, Type: semanticTokenVariable}, true
}

func skipShellString(line string, start, end int, quote byte) int {
	for col := start + 1; col < end; col++ {
		if quote == '"' && line[col] == '\\' {
			col++
			continue
		}
		if line[col] == quote {
			return col + 1
		}
	}
	return end
}

func doubleQuotedShellTokens(line string, lineNo, start, end int, shell string) (int, []semanticToken) {
	var tokens []semanticToken
	col := start + 1
	for col < end {
		switch line[col] {
		case '\\':
			col += 2
		case '"':
			return col + 1, tokens
		case '$':
			if token, ok := shellVariableToken(line, lineNo, col, end, shell); ok {
				tokens = append(tokens, token)
				col += token.Length
				continue
			}
			col++
		default:
			col++
		}
	}
	return end, tokens
}

func shellKeyword(shell, word string) bool {
	if shell == "fish" {
		return slices.Contains(fishKeywords, word)
	}
	return slices.Contains(shKeywords, word)
}

func commandContinuesAfterKeyword(shell, word string) bool {
	if shell == "fish" {
		return word == "and" || word == "or" || word == "not" || word == "command" || word == "if" || word == "while"
	}
	return word == "then" || word == "do" || word == "else" || word == "elif"
}

var shKeywords = []string{
	"case", "do", "done", "elif", "else", "esac", "fi", "for", "function",
	"if", "in", "select", "then", "time", "until", "while",
}

var fishKeywords = []string{
	"and", "begin", "case", "command", "else", "end", "for", "function",
	"if", "in", "not", "or", "set", "switch", "while",
}

func isShellSpace(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '\r'
}

func isShellOperator(ch byte) bool {
	return strings.ContainsRune("|&;()<>", rune(ch))
}

func isShellWordStart(ch byte) bool {
	return ch == '-' || ch == '_' || ch == '.' || ch == '/' || ch >= '0' && ch <= '9' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z'
}

func isShellWordPart(ch byte) bool {
	return isShellWordStart(ch) || ch == ':' || ch == '+'
}

type span struct {
	Start  int
	Length int
}

func placeholderSpans(line string) []span {
	var spans []span
	for i := 0; i < len(line); i++ {
		if line[i] != '{' {
			continue
		}
		end := i + 1
		if end >= len(line) || !isIdentStart(line[end]) {
			continue
		}
		end++
		for end < len(line) && isIdentPart(line[end]) {
			end++
		}
		if end < len(line) && line[end] == '}' {
			spans = append(spans, span{Start: i, Length: end - i + 1})
			i = end
		}
	}
	return spans
}

func encodeSemanticTokens(lines []string, tokens []semanticToken) []uint32 {
	var out []uint32
	prevLine, prevStart := 0, 0
	for _, token := range tokens {
		line := lineAt(lines, token.Line)
		start := byteToUTF16Offset(line, token.Start)
		length := byteToUTF16Offset(line, token.Start+token.Length) - start
		deltaLine := token.Line - prevLine
		deltaStart := start
		if deltaLine == 0 {
			deltaStart -= prevStart
		}
		out = append(out,
			uint32(deltaLine),
			uint32(deltaStart),
			uint32(length),
			uint32(token.Type),
			0,
		)
		prevLine = token.Line
		prevStart = start
	}
	return out
}

type semanticToken struct {
	Line   int
	Start  int
	Length int
	Type   int
}

const semanticTokenVariable = 0

const (
	semanticTokenKeyword = iota + 1
	semanticTokenFunction
	semanticTokenParameter
	semanticTokenOperator
	semanticTokenComment
)

func lineAt(lines []string, line int) string {
	if line < 0 || line >= len(lines) {
		return ""
	}
	return lines[line]
}

func linePrefix(line string, character int) string {
	if character < 0 {
		return ""
	}
	if character > len(line) {
		character = len(line)
	}
	return line[:character]
}

func utf16ToByteOffset(line string, character int) int {
	if character <= 0 {
		return 0
	}
	units := 0
	for offset, r := range line {
		width := utf16.RuneLen(r)
		if units+width > character {
			return offset
		}
		units += width
	}
	return len(line)
}

func byteToUTF16Offset(line string, offset int) int {
	if offset <= 0 {
		return 0
	}
	if offset > len(line) {
		offset = len(line)
	}
	units := 0
	for byteOffset, r := range line {
		if byteOffset >= offset {
			break
		}
		units += utf16.RuneLen(r)
	}
	return units
}

func openTablePrefix(prefix string) (string, bool) {
	trimmed := strings.TrimSpace(prefix)
	if strings.HasPrefix(trimmed, "[[") {
		trimmed = strings.TrimPrefix(trimmed, "[[")
	} else if strings.HasPrefix(trimmed, "[") {
		trimmed = strings.TrimPrefix(trimmed, "[")
	} else {
		return "", false
	}
	if strings.Contains(trimmed, "]") {
		return "", false
	}
	return strings.TrimSpace(trimmed), true
}

func completeTableHeader(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	trimmed = strings.TrimSuffix(strings.TrimSpace(strings.Split(trimmed, "#")[0]), "\r")
	switch {
	case strings.HasPrefix(trimmed, "[[") && strings.HasSuffix(trimmed, "]]"):
		return strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "[["), "]]")), true
	case strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]"):
		return strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "["), "]")), true
	default:
		return "", false
	}
}

func pairKey(line string) (string, bool) {
	before, _, ok := strings.Cut(line, "=")
	if !ok {
		return "", false
	}
	key := strings.TrimSpace(before)
	if key == "" || strings.ContainsAny(key, "[]") {
		return "", false
	}
	key = strings.Trim(key, `"'`)
	if key == "" {
		return "", false
	}
	return key, true
}

func keyBeforeValue(prefix string) (string, bool) {
	key, _, ok := strings.Cut(prefix, "=")
	if !ok {
		return "", false
	}
	key = strings.TrimSpace(key)
	if key == "" || strings.ContainsAny(key, "[]") {
		return "", false
	}
	return strings.Trim(key, `"'`), true
}

func placeholderPrefix(prefix string) (string, bool) {
	open := strings.LastIndexByte(prefix, '{')
	close := strings.LastIndexByte(prefix, '}')
	if open < 0 || close > open {
		return "", false
	}
	for _, ch := range prefix[open+1:] {
		if !(ch == '_' || ch >= '0' && ch <= '9' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z') {
			return "", false
		}
	}
	return prefix[open+1:], true
}

func currentRecipe(table string) string {
	if !strings.HasPrefix(table, "recipes.") {
		return ""
	}
	rest := strings.TrimPrefix(table, "recipes.")
	recipe, _, _ := strings.Cut(rest, ".")
	return recipe
}

func appendUnique(values []string, value string) []string {
	if value == "" || slices.Contains(values, value) {
		return values
	}
	return append(values, value)
}

func uniqueSorted(values []string) []string {
	var out []string
	for _, value := range values {
		out = appendUnique(out, value)
	}
	slices.Sort(out)
	return out
}

func isIdentStart(ch byte) bool {
	return ch == '_' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z'
}

func isIdentPart(ch byte) bool {
	return isIdentStart(ch) || ch >= '0' && ch <= '9'
}
