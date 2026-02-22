package lsp

import "context"

type LspService interface {
	Init(ctx context.Context)
	Shutdown(ctx context.Context)
	ForceShutdown()

	Clients() map[string]*Client
	ClientsCh() <-chan *Client
	ClientsForFile(filePath string) []*Client

	NotifyOpenFile(ctx context.Context, filePath string)
	WaitForDiagnostics(ctx context.Context, filePath string)
	FormatDiagnostics(filePath string) string
}
