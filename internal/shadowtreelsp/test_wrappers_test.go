package shadowtreelsp

import (
	"context"
	"strings"
)

func completionsAt(ctx context.Context, text string, pos lspPosition) []completion {
	return completionsAtWithOptions(ctx, text, pos, completionOptions{})
}

func documentDiagnostics(ctx context.Context, text string) []lspDiagnostic {
	return documentDiagnosticsWithOptions(ctx, text, diagnosticOptions{})
}

func completionResult(ctx context.Context, text string, position lspPosition) map[string]any {
	return completionResultWithOptions(ctx, text, position, completionOptions{})
}

func keyTextEdit(text string, position lspPosition, label, insertText string) map[string]any {
	lines := strings.Split(text, "\n")
	return keyTextEditLine(lineAt(lines, position.Line), position, label, insertText)
}

func quotedValueTextEdit(text string, position lspPosition, value string) map[string]any {
	lines := strings.Split(text, "\n")
	return quotedValueTextEditLine(lineAt(lines, position.Line), position, value)
}

func placeholderTextEdit(text string, position lspPosition, label string) map[string]any {
	lines := strings.Split(text, "\n")
	return placeholderTextEditLine(lineAt(lines, position.Line), position, label)
}

func tableTextEdit(text string, position lspPosition, insertText string) map[string]any {
	lines := strings.Split(text, "\n")
	return tableTextEditLine(lineAt(lines, position.Line), position, insertText)
}
