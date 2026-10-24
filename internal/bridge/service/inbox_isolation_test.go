package service

import "github.com/opencode-ai/opencode/internal/inbox"

// Every router built in this package's tests gets its own inbox, so tests
// that reuse session ids such as "S1" never see each other's input through
// the process-wide inbox.Default().
func init() {
	routerInbox = func() *inbox.Box { return inbox.New(interactiveInboundBufferCap) }
}
