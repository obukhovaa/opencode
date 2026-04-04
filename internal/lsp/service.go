package lsp

import (
	"context"

	"github.com/opencode-ai/opencode/internal/pubsub"
)

type LSPServerEventType string

const (
	LSPServerReady LSPServerEventType = "ready"
	LSPServerError LSPServerEventType = "error"
)

type LSPServerEvent struct {
	Type       LSPServerEventType
	ServerName string
}

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

	pubsub.Suscriber[LSPServerEvent]
}
