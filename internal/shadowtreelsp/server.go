package shadowtreelsp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
)

// Serve runs the Shadowtree language server over stdio.
func Serve(ctx context.Context, input io.Reader, output io.Writer) error {
	server := &server{
		ctx:       ctx,
		input:     bufio.NewReader(input),
		output:    output,
		documents: map[string]string{},
	}
	return server.serve()
}

type server struct {
	ctx       context.Context
	input     *bufio.Reader
	output    io.Writer
	documents map[string]string
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type lspPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

func (server *server) serve() error {
	for {
		body, err := readMessage(server.input)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		var msg rpcMessage
		if err := json.Unmarshal(body, &msg); err != nil {
			return err
		}
		if msg.Method == "exit" {
			return nil
		}
		result, err := server.handle(msg)
		if len(msg.ID) == 0 {
			continue
		}
		if err != nil {
			if writeErr := writeMessage(server.output, rpcResponse{
				JSONRPC: "2.0",
				ID:      msg.ID,
				Error:   &rpcError{Code: -32603, Message: err.Error()},
			}); writeErr != nil {
				return writeErr
			}
			continue
		}
		if err := writeMessage(server.output, rpcResponse{
			JSONRPC: "2.0",
			ID:      msg.ID,
			Result:  result,
		}); err != nil {
			return err
		}
	}
}

func (server *server) handle(msg rpcMessage) (any, error) {
	switch msg.Method {
	case "initialize":
		return initializeResult(), nil
	case "shutdown":
		return nil, nil
	case "textDocument/didOpen":
		var params didOpenParams
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			return nil, err
		}
		server.documents[params.TextDocument.URI] = params.TextDocument.Text
		if err := server.publishDiagnostics(server.ctx, params.TextDocument.URI, params.TextDocument.Text, params.TextDocument.Version); err != nil {
			return nil, err
		}
		return nil, nil
	case "textDocument/didChange":
		var params didChangeParams
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			return nil, err
		}
		if len(params.ContentChanges) > 0 {
			text := server.documents[params.TextDocument.URI]
			for _, change := range params.ContentChanges {
				text = applyContentChange(text, change)
			}
			server.documents[params.TextDocument.URI] = text
			if err := server.publishDiagnostics(server.ctx, params.TextDocument.URI, text, params.TextDocument.Version); err != nil {
				return nil, err
			}
		}
		return nil, nil
	case "textDocument/didClose":
		var params didCloseParams
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			return nil, err
		}
		delete(server.documents, params.TextDocument.URI)
		if err := server.publishDiagnostics(server.ctx, params.TextDocument.URI, "", nil); err != nil {
			return nil, err
		}
		return nil, nil
	case "textDocument/completion":
		var params completionParams
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			return nil, err
		}
		return completionResultWithOptions(server.ctx, server.documents[params.TextDocument.URI], params.Position, completionOptionsForURI(params.TextDocument.URI)), nil
	case "textDocument/semanticTokens/full":
		var params semanticTokensParams
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			return nil, err
		}
		return map[string]any{"data": semanticTokens(server.documents[params.TextDocument.URI])}, nil
	default:
		return nil, nil
	}
}

func completionOptionsForURI(uri string) completionOptions {
	path, ok := lspFilePath(uri)
	if !ok {
		return completionOptions{}
	}
	return completionOptions{
		Dir:        filepath.Dir(path),
		ConfigPath: path,
	}
}

func lspConfigDir(uri string) (string, bool) {
	path, ok := lspFilePath(uri)
	if !ok {
		return "", false
	}
	return filepath.Dir(path), true
}

func lspFilePath(uri string) (string, bool) {
	parsed, err := url.Parse(uri)
	if err != nil || parsed.Scheme != "file" {
		return "", false
	}
	return filepath.FromSlash(parsed.Path), true
}

func initializeResult() map[string]any {
	return map[string]any{
		"capabilities": map[string]any{
			"textDocumentSync": map[string]any{
				"openClose": true,
				"change":    2,
			},
			"completionProvider": map[string]any{
				"triggerCharacters": []string{"[", ".", "{", "=", "\"", "'", "@", ",", ":"},
			},
			"semanticTokensProvider": map[string]any{
				"legend": map[string]any{
					"tokenTypes":     []string{"variable", "keyword", "function", "parameter", "operator", "comment", "recipeReference", "string"},
					"tokenModifiers": []string{},
				},
				"full": true,
			},
		},
		"serverInfo": map[string]any{
			"name":    "shadowtree-lsp",
			"version": "0.1.0",
		},
	}
}

type didOpenParams struct {
	TextDocument struct {
		URI     string `json:"uri"`
		Version *int   `json:"version,omitempty"`
		Text    string `json:"text"`
	} `json:"textDocument"`
}

type didChangeParams struct {
	TextDocument struct {
		URI     string `json:"uri"`
		Version *int   `json:"version,omitempty"`
	} `json:"textDocument"`
	ContentChanges []contentChange `json:"contentChanges"`
}

type contentChange struct {
	Range *lspTextRange `json:"range,omitempty"`
	Text  string        `json:"text"`
}

type lspTextRange struct {
	Start lspPosition `json:"start"`
	End   lspPosition `json:"end"`
}

type didCloseParams struct {
	TextDocument struct {
		URI string `json:"uri"`
	} `json:"textDocument"`
}

type completionParams struct {
	TextDocument struct {
		URI string `json:"uri"`
	} `json:"textDocument"`
	Position lspPosition `json:"position"`
}

type semanticTokensParams struct {
	TextDocument struct {
		URI string `json:"uri"`
	} `json:"textDocument"`
}

func completionResult(ctx context.Context, text string, position lspPosition) map[string]any {
	return completionResultWithOptions(ctx, text, position, completionOptions{})
}

func completionResultWithOptions(ctx context.Context, text string, position lspPosition, opts completionOptions) map[string]any {
	lines := strings.Split(text, "\n")
	bytePosition := lspToBytePosition(lines, position)
	completions := completionsAtWithOptions(ctx, text, bytePosition, opts)
	items := make([]map[string]any, 0, len(completions))
	for _, item := range completions {
		out := map[string]any{
			"label":      item.Label,
			"kind":       item.Kind,
			"detail":     item.Detail,
			"insertText": item.InsertText,
		}
		switch {
		case item.Edit != nil:
			lines := strings.Split(text, "\n")
			line := lineAt(lines, bytePosition.Line)
			out["textEdit"] = textEdit(line, bytePosition.Line, item.Edit.Start, item.Edit.End, item.InsertText)
		case item.Quote:
			out["textEdit"] = quotedValueTextEdit(text, bytePosition, item.Label)
		case item.Detail == "Shadowtree placeholder":
			out["textEdit"] = placeholderTextEdit(text, bytePosition, item.Label)
		case item.RecipeReference:
			out["textEdit"] = recipeReferenceTextEdit(text, bytePosition, item.InsertText)
		case inTableHeader(text, bytePosition):
			out["textEdit"] = tableTextEdit(text, bytePosition, item.InsertText)
		case item.Kind == completionKindKeyword:
			out["textEdit"] = keyTextEdit(text, bytePosition, item.Label, item.InsertText)
		}
		items = append(items, out)
	}
	return map[string]any{
		"isIncomplete": false,
		"items":        items,
	}
}

func recipeReferenceTextEdit(text string, position lspPosition, name string) map[string]any {
	lines := strings.Split(text, "\n")
	line := lineAt(lines, position.Line)
	prefix := linePrefix(line, position.Character)
	at := strings.LastIndexByte(prefix, '@')
	if at < 0 {
		return textEdit(line, position.Line, position.Character, position.Character, "@"+name)
	}
	start := at + 1
	end := position.Character
	for end < len(line) && isRecipeReferenceNameByte(line[end]) {
		end++
	}
	return textEdit(line, position.Line, start, end, name)
}

func keyTextEdit(text string, position lspPosition, label, insertText string) map[string]any {
	lines := strings.Split(text, "\n")
	line := lineAt(lines, position.Line)
	start := wordStart(line, position.Character)
	end := wordEnd(line, position.Character)
	newText := insertText
	if hasAssignmentAfter(line, end) {
		newText = label
	}
	return textEdit(line, position.Line, start, end, newText)
}

func quotedValueTextEdit(text string, position lspPosition, value string) map[string]any {
	lines := strings.Split(text, "\n")
	line := lineAt(lines, position.Line)
	start := strings.IndexByte(line, '=')
	if start < 0 {
		start = position.Character
	} else {
		start++
		for start < len(line) && (line[start] == ' ' || line[start] == '\t') {
			start++
		}
	}
	end := start
	newText := `"` + value + `"`
	if start < len(line) && (line[start] == '"' || line[start] == '\'') {
		quote := line[start]
		start++
		end = start
		for end < len(line) && line[end] != quote {
			end++
		}
		newText = value
	}
	if position.Character > end {
		end = position.Character
	}
	return map[string]any{
		"range":   lspRange(line, position.Line, start, end),
		"newText": newText,
	}
}

func placeholderTextEdit(text string, position lspPosition, label string) map[string]any {
	lines := strings.Split(text, "\n")
	line := lineAt(lines, position.Line)
	prefix := linePrefix(line, position.Character)
	open := strings.LastIndexByte(prefix, '{')
	if open < 0 {
		return textEdit(line, position.Line, position.Character, position.Character, label)
	}
	name := strings.TrimSuffix(strings.TrimPrefix(label, "{"), "}")
	start := open + 1
	end := position.Character
	newText := name + "}"
	if close := strings.IndexByte(line[position.Character:], '}'); close >= 0 {
		end = position.Character + close
		newText = name
	}
	return textEdit(line, position.Line, start, end, newText)
}

func tableTextEdit(text string, position lspPosition, insertText string) map[string]any {
	lines := strings.Split(text, "\n")
	line := lineAt(lines, position.Line)
	start := tableSegmentStart(linePrefix(line, position.Character))
	end := position.Character
	if close := strings.IndexByte(line[position.Character:], ']'); close >= 0 {
		end = position.Character + close
		if before, ok := strings.CutSuffix(insertText, "]"); ok {
			insertText = before
		}
	}
	return textEdit(line, position.Line, start, end, insertText)
}

func inTableHeader(text string, position lspPosition) bool {
	lines := strings.Split(text, "\n")
	_, ok := openTablePrefix(linePrefix(lineAt(lines, position.Line), position.Character))
	return ok
}

func tableSegmentStart(prefix string) int {
	start := strings.LastIndexByte(prefix, '[')
	if dot := strings.LastIndexByte(prefix, '.'); dot > start {
		start = dot
	}
	return start + 1
}

func wordStart(line string, character int) int {
	if character > len(line) {
		character = len(line)
	}
	for character > 0 && isBareKeyByte(line[character-1]) {
		character--
	}
	return character
}

func wordEnd(line string, character int) int {
	if character < 0 {
		character = 0
	}
	for character < len(line) && isBareKeyByte(line[character]) {
		character++
	}
	return character
}

func hasAssignmentAfter(line string, start int) bool {
	for i := start; i < len(line); i++ {
		switch line[i] {
		case ' ', '\t':
			continue
		case '=':
			return true
		default:
			return false
		}
	}
	return false
}

func isBareKeyByte(ch byte) bool {
	return ch == '_' || ch == '-' || ch >= '0' && ch <= '9' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z'
}

func textEdit(lineText string, line, start, end int, newText string) map[string]any {
	return map[string]any{
		"range":   lspRange(lineText, line, start, end),
		"newText": newText,
	}
}

func lspRange(lineText string, line, start, end int) map[string]any {
	return map[string]any{
		"start": lspPosition{Line: line, Character: byteToUTF16Offset(lineText, start)},
		"end":   lspPosition{Line: line, Character: byteToUTF16Offset(lineText, end)},
	}
}

func lspToBytePosition(lines []string, position lspPosition) lspPosition {
	line := lineAt(lines, position.Line)
	return lspPosition{
		Line:      position.Line,
		Character: utf16ToByteOffset(line, position.Character),
	}
}

func applyContentChange(text string, change contentChange) string {
	if change.Range == nil {
		return change.Text
	}
	start, end := changeByteRange(text, *change.Range)
	return text[:start] + change.Text + text[end:]
}

func changeByteRange(text string, editRange lspTextRange) (int, int) {
	lines := strings.Split(text, "\n")
	lineStart := lineByteStart(lines, editRange.Start.Line)
	lineEnd := lineByteStart(lines, editRange.End.Line)
	startLine := lineAt(lines, editRange.Start.Line)
	endLine := lineAt(lines, editRange.End.Line)
	start := lineStart + utf16ToByteOffset(startLine, editRange.Start.Character)
	end := lineEnd + utf16ToByteOffset(endLine, editRange.End.Character)
	if start > len(text) {
		start = len(text)
	}
	if end > len(text) {
		end = len(text)
	}
	if end < start {
		end = start
	}
	return start, end
}

func lineByteStart(lines []string, line int) int {
	if line <= 0 {
		return 0
	}
	if line > len(lines) {
		line = len(lines)
	}
	start := 0
	for i := 0; i < line; i++ {
		start += len(lines[i])
		if i < len(lines)-1 {
			start++
		}
	}
	return start
}

func readMessage(reader *bufio.Reader) ([]byte, error) {
	var contentLength int
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(key), "Content-Length") {
			parsed, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return nil, err
			}
			contentLength = parsed
		}
	}
	if contentLength <= 0 {
		return nil, fmt.Errorf("missing content length")
	}
	body := make([]byte, contentLength)
	_, err := io.ReadFull(reader, body)
	return body, err
}

func writeMessage(writer io.Writer, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var out bytes.Buffer
	fmt.Fprintf(&out, "Content-Length: %d\r\n\r\n", len(body))
	out.Write(body)
	_, err = writer.Write(out.Bytes())
	return err
}
