package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"strings"
	"time"

	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/lsp/protocol"
)

func notifyLspOpenFile(ctx context.Context, filePath string, lsps map[string]*lsp.Client) {
	for _, client := range lsps {
		err := client.OpenFile(ctx, filePath)
		if err != nil {
			continue
		}
	}
}

func waitForLspDiagnostics(ctx context.Context, filePath string, lsps map[string]*lsp.Client) {
	if len(lsps) == 0 {
		return
	}

	diagChan := make(chan struct{}, 1)

	for _, client := range lsps {
		originalDiags := make(map[protocol.DocumentUri][]protocol.Diagnostic)
		maps.Copy(originalDiags, client.GetDiagnostics())

		handler := func(params json.RawMessage) {
			lsp.HandleDiagnostics(client, params)
			var diagParams protocol.PublishDiagnosticsParams
			if err := json.Unmarshal(params, &diagParams); err != nil {
				return
			}

			if diagParams.URI.Path() == filePath || hasDiagnosticsChanged(client.GetDiagnostics(), originalDiags) {
				select {
				case diagChan <- struct{}{}:
				default:
				}
			}
		}

		client.RegisterNotificationHandler("textDocument/publishDiagnostics", handler)

		if client.IsFileOpen(filePath) {
			err := client.NotifyChange(ctx, filePath)
			if err != nil {
				continue
			}
		} else {
			err := client.OpenFile(ctx, filePath)
			if err != nil {
				continue
			}
		}
	}

	select {
	case <-diagChan:
	case <-time.After(5 * time.Second):
	case <-ctx.Done():
	}
}

func hasDiagnosticsChanged(current, original map[protocol.DocumentUri][]protocol.Diagnostic) bool {
	for uri, diags := range current {
		origDiags, exists := original[uri]
		if !exists || len(diags) != len(origDiags) {
			return true
		}
	}
	return false
}

// getDiagnostics collects and formats diagnostics from all LSP clients.
// Used by file-mutating tools (edit, write, patch) and view to provide
// inline feedback to the LLM after each operation.
func getDiagnostics(filePath string, lsps map[string]*lsp.Client) string {
	fileDiagnostics := []string{}
	projectDiagnostics := []string{}

	formatDiagnostic := func(pth string, diagnostic protocol.Diagnostic, source string) string {
		severity := "Info"
		switch diagnostic.Severity {
		case protocol.SeverityError:
			severity = "Error"
		case protocol.SeverityWarning:
			severity = "Warn"
		case protocol.SeverityHint:
			severity = "Hint"
		}

		location := fmt.Sprintf("%s:%d:%d", pth, diagnostic.Range.Start.Line+1, diagnostic.Range.Start.Character+1)

		sourceInfo := ""
		if diagnostic.Source != "" {
			sourceInfo = diagnostic.Source
		} else if source != "" {
			sourceInfo = source
		}

		codeInfo := ""
		if diagnostic.Code != nil {
			codeInfo = fmt.Sprintf("[%v]", diagnostic.Code)
		}

		tagsInfo := ""
		if len(diagnostic.Tags) > 0 {
			tags := []string{}
			for _, tag := range diagnostic.Tags {
				switch tag {
				case protocol.Unnecessary:
					tags = append(tags, "unnecessary")
				case protocol.Deprecated:
					tags = append(tags, "deprecated")
				}
			}
			if len(tags) > 0 {
				tagsInfo = fmt.Sprintf(" (%s)", strings.Join(tags, ", "))
			}
		}

		return fmt.Sprintf("%s: %s [%s]%s%s %s",
			severity,
			location,
			sourceInfo,
			codeInfo,
			tagsInfo,
			diagnostic.Message)
	}

	for lspName, client := range lsps {
		diagnostics := client.GetDiagnostics()
		if len(diagnostics) > 0 {
			for location, diags := range diagnostics {
				isCurrentFile := location.Path() == filePath

				for _, diag := range diags {
					formattedDiag := formatDiagnostic(location.Path(), diag, lspName)

					if isCurrentFile {
						fileDiagnostics = append(fileDiagnostics, formattedDiag)
					} else {
						projectDiagnostics = append(projectDiagnostics, formattedDiag)
					}
				}
			}
		}
	}

	sort.Slice(fileDiagnostics, func(i, j int) bool {
		iIsError := strings.HasPrefix(fileDiagnostics[i], "Error")
		jIsError := strings.HasPrefix(fileDiagnostics[j], "Error")
		if iIsError != jIsError {
			return iIsError // Errors come first
		}
		return fileDiagnostics[i] < fileDiagnostics[j] // Then alphabetically
	})

	sort.Slice(projectDiagnostics, func(i, j int) bool {
		iIsError := strings.HasPrefix(projectDiagnostics[i], "Error")
		jIsError := strings.HasPrefix(projectDiagnostics[j], "Error")
		if iIsError != jIsError {
			return iIsError
		}
		return projectDiagnostics[i] < projectDiagnostics[j]
	})

	output := ""

	if len(fileDiagnostics) > 0 {
		output += "\n<file_diagnostics>\n"
		if len(fileDiagnostics) > 10 {
			output += strings.Join(fileDiagnostics[:10], "\n")
			output += fmt.Sprintf("\n... and %d more diagnostics", len(fileDiagnostics)-10)
		} else {
			output += strings.Join(fileDiagnostics, "\n")
		}
		output += "\n</file_diagnostics>\n"
	}

	if len(projectDiagnostics) > 0 {
		output += "\n<project_diagnostics>\n"
		if len(projectDiagnostics) > 10 {
			output += strings.Join(projectDiagnostics[:10], "\n")
			output += fmt.Sprintf("\n... and %d more diagnostics", len(projectDiagnostics)-10)
		} else {
			output += strings.Join(projectDiagnostics, "\n")
		}
		output += "\n</project_diagnostics>\n"
	}

	if len(fileDiagnostics) > 0 || len(projectDiagnostics) > 0 {
		fileErrors := countSeverity(fileDiagnostics, "Error")
		fileWarnings := countSeverity(fileDiagnostics, "Warn")
		projectErrors := countSeverity(projectDiagnostics, "Error")
		projectWarnings := countSeverity(projectDiagnostics, "Warn")

		output += "\n<diagnostic_summary>\n"
		output += fmt.Sprintf("Current file: %d errors, %d warnings\n", fileErrors, fileWarnings)
		output += fmt.Sprintf("Project: %d errors, %d warnings\n", projectErrors, projectWarnings)
		output += "</diagnostic_summary>\n"
	}

	return output
}

func countSeverity(diagnostics []string, severity string) int {
	count := 0
	for _, diag := range diagnostics {
		if strings.HasPrefix(diag, severity) {
			count++
		}
	}
	return count
}
