package agent

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/version"
)

// mcpClientIdleTimeout is how long a pooled client with no call in progress is
// kept before it is closed, unless its server sets clientIdleTimeoutSeconds.
const mcpClientIdleTimeout = 10 * time.Minute

// Vars rather than consts so tests can shorten them; never mutated at runtime.
var (
	// mcpCloseGrace is how long a stdio server gets to exit after its stdin
	// closes — and again after SIGTERM — before the next signal.
	mcpCloseGrace = 2 * time.Second
	// mcpPoolSweepInterval is how often the pool looks for idle clients.
	mcpPoolSweepInterval = 30 * time.Second
)

var (
	errMCPPoolClosed       = errors.New("MCP client pool is shut down")
	errMCPHandshakeTimeout = errors.New("MCP handshake did not complete")
)

// mcpClientFactory builds and starts a transport-level client for one server.
// lifetime bounds the client itself (SSE keeps its event stream under it), not
// the call that triggered the connect. Overridable so pool tests can inject a
// fake client.
type mcpClientFactory func(lifetime context.Context, name string, m config.MCPServer, headers map[string]string) (MCPClient, *mcpProc, error)

// mcpConn is one connected, initialized MCP client plus the pool's
// bookkeeping around it.
type mcpConn struct {
	server string
	// key is the connection identity (mcpConnKey); "" for an unpooled conn,
	// which serves exactly one call.
	key  string
	idle time.Duration

	// ready is closed once the connect finished; client, proc, cancel and err
	// are written before it closes and only read after.
	ready  chan struct{}
	client MCPClient
	proc   *mcpProc
	cancel context.CancelFunc
	err    error

	// Guarded by mcpClientPool.mu.
	refs     int
	lastUsed time.Time
	// retired: out of the map, handed to no new caller, closed when refs hits 0.
	retired bool
	closing bool
}

// mcpClientPool keeps one connected client per MCP server and connection
// identity, so tool calls stop paying a process spawn (stdio) or an
// initialize + session DELETE round trip (HTTP) each, and a tool result never
// waits for a client to close. See openspec/changes/mcp-client-pool/design.md.
type mcpClientPool struct {
	// baseCtx owns every client's lifetime, for the same reason it owns the
	// tools/list cache fetches: a client is shared, so it must never live
	// under one caller's request-scoped context.
	baseCtx   context.Context
	newClient mcpClientFactory

	mu      sync.Mutex
	conns   map[string]*mcpConn
	closed  bool
	stopped chan struct{}
	// closing counts background closes still running, so Shutdown can wait.
	closing        int
	janitorStarted bool
}

func newMCPClientPool(baseCtx context.Context) *mcpClientPool {
	return &mcpClientPool{
		baseCtx:   baseCtx,
		newClient: newMCPClient,
		conns:     map[string]*mcpConn{},
		stopped:   make(chan struct{}),
	}
}

// resolveClientIdleTimeout returns how long server m's pooled client is kept
// after its last call; negative means the server opted out of reuse.
func resolveClientIdleTimeout(m config.MCPServer) time.Duration {
	switch {
	case m.ClientIdleTimeoutSeconds < 0:
		return -1
	case m.ClientIdleTimeoutSeconds > 0:
		return time.Duration(m.ClientIdleTimeoutSeconds) * time.Second
	default:
		return mcpClientIdleTimeout
	}
}

// mcpConnKey is the connection identity two calls must share to share a
// client: the server's launch configuration plus, for HTTP and SSE, every
// request header. Headers are part of it — rather than injected per request on
// one shared session — because a server may bind identity to the session at
// initialize, so a per-flow Authorization override or a bridge peer must
// never ride on a session another identity opened. Hashed, so no token sits
// in the key; length-prefixed, so no two field lists hash alike.
func mcpConnKey(name string, m config.MCPServer, headers map[string]string) string {
	h := sha256.New()
	field := func(s string) { fmt.Fprintf(h, "%d:%s", len(s), s) }
	field(string(m.Type))
	field(m.Command)
	field(m.URL)
	for _, a := range m.Args {
		field(a)
	}
	field("env")
	for _, e := range m.Env {
		field(e)
	}
	if m.Type == config.MCPHttp || m.Type == config.MCPSse {
		keys := make([]string, 0, len(headers))
		for k := range headers {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		field("headers")
		for _, k := range keys {
			field(strings.ToLower(k))
			field(headers[k])
		}
	}
	return name + "\x00" + hex.EncodeToString(h.Sum(nil))
}

// acquire returns a connected client for server name under the headers ctx
// resolves to, connecting one if none is pooled. The caller must release it.
// ctx bounds only the wait: the connect itself runs under the pool's
// lifetime, so a caller giving up cannot fail it for anyone else.
func (p *mcpClientPool) acquire(ctx context.Context, name string, m config.MCPServer) (*mcpConn, error) {
	headers := resolvePeerHeader(ctx, m.PeerHeader, resolveMCPHeaders(ctx, name, m.Headers))
	idle := resolveClientIdleTimeout(m)

	p.mu.Lock()
	if p.closed || p.baseCtx.Err() != nil {
		p.mu.Unlock()
		return nil, errMCPPoolClosed
	}
	var e *mcpConn
	if idle >= 0 {
		key := mcpConnKey(name, m, headers)
		e = p.conns[key]
		if e == nil {
			e = &mcpConn{server: name, key: key, idle: idle, ready: make(chan struct{}), lastUsed: time.Now()}
			p.conns[key] = e
			go p.dial(e, m, headers)
		}
		if !p.janitorStarted {
			p.janitorStarted = true
			go p.janitor()
		}
	} else {
		e = &mcpConn{server: name, ready: make(chan struct{})}
		go p.dial(e, m, headers)
	}
	e.refs++
	p.mu.Unlock()

	select {
	case <-e.ready:
	case <-ctx.Done():
		p.release(e, false)
		return nil, ctx.Err()
	}
	if e.err != nil {
		p.release(e, false)
		return nil, e.err
	}
	return e, nil
}

// release hands e back. evict retires it: new callers get a fresh client,
// calls still running on e finish, and e closes once the last one releases.
func (p *mcpClientPool) release(e *mcpConn, evict bool) {
	p.mu.Lock()
	e.refs--
	e.lastUsed = time.Now()
	if evict && !e.retired {
		e.retired = true
		if e.key != "" && p.conns[e.key] == e {
			delete(p.conns, e.key)
		}
	}
	closeNow := e.refs == 0 && (e.retired || e.key == "") && !e.closing
	if closeNow {
		e.closing = true
	}
	p.mu.Unlock()
	if closeNow {
		p.closeConn(e)
	}
}

// dial connects e: build and start the transport, then initialize, with the
// handshake bounded by mcpInitTimeout.
func (p *mcpClientPool) dial(e *mcpConn, m config.MCPServer, headers map[string]string) {
	lifetime, cancel := context.WithCancel(p.baseCtx)
	initCtx, cancelInit := context.WithTimeout(lifetime, mcpInitTimeout)
	defer cancelInit()

	// A transport that starts under lifetime rather than initCtx — SSE opens
	// its event stream there, since the stream must outlive the handshake —
	// is still bounded: once the budget is gone the lifetime is cancelled too,
	// which aborts the start. settled guards the window where the budget
	// expires just as the connect succeeds.
	var settleMu sync.Mutex
	settled := false
	stopWatch := context.AfterFunc(initCtx, func() {
		settleMu.Lock()
		defer settleMu.Unlock()
		if !settled {
			cancel()
		}
	})
	c, proc, err := p.newClient(lifetime, e.server, m, headers)
	if err == nil {
		_, err = c.Initialize(initCtx, mcpInitializeRequest())
	}
	stopWatch()
	settleMu.Lock()
	settled = true
	if err == nil && lifetime.Err() != nil {
		err = lifetime.Err()
	}
	settleMu.Unlock()

	if err != nil {
		if errors.Is(initCtx.Err(), context.DeadlineExceeded) && p.baseCtx.Err() == nil {
			err = fmt.Errorf("%w within %s: %v", errMCPHandshakeTimeout, mcpInitTimeout, err)
		}
		logging.Error("Error connecting MCP client", "server", e.server, "cause", err)
		p.mu.Lock()
		e.retired = true
		if e.key != "" && p.conns[e.key] == e {
			delete(p.conns, e.key)
		}
		p.mu.Unlock()
		e.err = err
		close(e.ready)
		if c != nil {
			p.runClose(func() {
				closeMCPClient(c, proc, e.server, mcpInitTimeout)
				cancel()
			})
		} else {
			cancel()
		}
		return
	}
	e.client, e.proc, e.cancel = c, proc, cancel
	close(e.ready)
}

// closeConn closes e in the background once its connect has finished.
func (p *mcpClientPool) closeConn(e *mcpConn) {
	p.runClose(func() {
		<-e.ready
		if e.client != nil {
			closeMCPClient(e.client, e.proc, e.server, mcpInitTimeout)
		}
		if e.cancel != nil {
			e.cancel()
		}
	})
}

// runClose runs a close off the caller's path, counted so Shutdown can wait.
func (p *mcpClientPool) runClose(fn func()) {
	p.mu.Lock()
	p.closing++
	p.mu.Unlock()
	go func() {
		defer func() {
			p.mu.Lock()
			p.closing--
			p.mu.Unlock()
		}()
		fn()
	}()
}

// janitor closes idle clients, and the whole pool once the registry's
// context ends.
func (p *mcpClientPool) janitor() {
	t := time.NewTicker(mcpPoolSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-p.stopped:
			return
		case <-p.baseCtx.Done():
			ctx, cancel := context.WithTimeout(context.Background(), 2*mcpCloseGrace+time.Second)
			p.shutdown(ctx)
			cancel()
			return
		case now := <-t.C:
			p.sweep(now)
		}
	}
}

// sweep closes every connected client with no call in progress that has
// been idle longer than its server's timeout.
func (p *mcpClientPool) sweep(now time.Time) {
	p.mu.Lock()
	var idle []*mcpConn
	for key, e := range p.conns {
		if e.refs > 0 || !isClosedChan(e.ready) || now.Sub(e.lastUsed) < e.idle {
			continue
		}
		e.retired, e.closing = true, true
		delete(p.conns, key)
		idle = append(idle, e)
	}
	p.mu.Unlock()
	for _, e := range idle {
		logging.Debug("Closing idle MCP client", "server", e.server, "idle", now.Sub(e.lastUsed))
		p.closeConn(e)
	}
}

// shutdown stops handing out clients, closes every pooled one — a client
// still in use closes when its call releases it — and waits for the closes
// until ctx is done.
func (p *mcpClientPool) shutdown(ctx context.Context) {
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		close(p.stopped)
	}
	var idle []*mcpConn
	for key, e := range p.conns {
		e.retired = true
		delete(p.conns, key)
		if e.refs == 0 && !e.closing {
			e.closing = true
			idle = append(idle, e)
		}
	}
	p.mu.Unlock()
	for _, e := range idle {
		p.closeConn(e)
	}

	t := time.NewTicker(10 * time.Millisecond)
	defer t.Stop()
	for {
		p.mu.Lock()
		pending := p.closing
		p.mu.Unlock()
		if pending == 0 {
			return
		}
		select {
		case <-ctx.Done():
			logging.Warn("MCP clients still closing at shutdown; not waiting further", "pending", pending)
			return
		case <-t.C:
		}
	}
}

func isClosedChan(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func mcpInitializeRequest() mcp.InitializeRequest {
	req := mcp.InitializeRequest{}
	req.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	req.Params.ClientInfo = mcp.Implementation{
		Name:    "opencode",
		Version: version.Version,
	}
	return req
}

// newMCPClient builds and starts the transport-level client for server m.
func newMCPClient(lifetime context.Context, name string, m config.MCPServer, headers map[string]string) (MCPClient, *mcpProc, error) {
	var (
		c    *client.Client
		proc *mcpProc
		err  error
	)
	switch m.Type {
	case config.MCPSse:
		c, err = client.NewSSEMCPClient(m.URL, client.WithHeaders(headers))
	case config.MCPHttp:
		c, err = client.NewStreamableHttpClient(m.URL, transport.WithHTTPHeaders(headers))
	default:
		if m.Command == "" {
			return nil, nil, fmt.Errorf("MCP server %q has no command", name)
		}
		// Spawned through our own command func: it is the only way to keep
		// the process handle mcp-go does not expose, which closeMCPClient
		// needs to stop a server that ignores stdin EOF.
		proc = &mcpProc{}
		c, err = client.NewStdioMCPClientWithOptions(m.Command, m.Env, m.Args, transport.WithCommandFunc(proc.command))
		if err == nil {
			drainMCPStderr(c, name)
		}
	}
	if err != nil {
		return nil, nil, err
	}
	if err := c.Start(lifetime); err != nil {
		// Close anyway, in the background: SSE's Start can fail after its
		// reader goroutine runs, and a nil return would leave nothing to close
		// the transport with.
		go closeMCPClient(c, proc, name, mcpInitTimeout)
		return nil, nil, err
	}
	return c, proc, nil
}

// drainMCPStderr reads a stdio server's stderr into the debug log. mcp-go
// connects stderr to a pipe and never reads it; a long-lived server that logs
// would block once the pipe buffer filled.
func drainMCPStderr(c *client.Client, name string) {
	r, ok := client.GetStderr(c)
	if !ok || r == nil {
		return
	}
	go func() {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 4096), 1<<20)
		for sc.Scan() {
			logging.Debug("MCP server stderr", "server", name, "line", truncateStr(sc.Text(), 500))
		}
		// A line past the scanner's limit ends the scan; keep draining.
		_, _ = io.Copy(io.Discard, r)
	}()
}

// mcpProc holds a stdio server's process, captured when mcp-go asks for the
// command to launch.
type mcpProc struct {
	mu  sync.Mutex
	cmd *exec.Cmd
}

func (p *mcpProc) command(_ context.Context, command string, env []string, args []string) (*exec.Cmd, error) {
	cmd := exec.Command(command, args...)
	cmd.Env = append(os.Environ(), env...)
	setMCPProcessGroup(cmd)
	p.mu.Lock()
	p.cmd = cmd
	p.mu.Unlock()
	return cmd, nil
}

func (p *mcpProc) signal(sig syscall.Signal) {
	p.mu.Lock()
	cmd := p.cmd
	p.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	signalMCPProcessGroup(cmd, sig)
}

// closeMCPClient closes an MCP client; callers run it off the tool-call path.
//
// transport.Stdio.Close closes stdin and then blocks in cmd.Wait(), honouring
// no context. A cooperative server exits on stdin EOF, but some take 10–20 s
// and some never do. For a stdio server (proc != nil) that is still running
// after mcpCloseGrace, the process group gets SIGTERM, then SIGKILL. Signals
// go out only while Close is still inside cmd.Wait(), i.e. before the process
// is reaped, so its PID cannot have been reused. Any other close (an HTTP
// session DELETE) that outlives budget is abandoned and logged.
func closeMCPClient(c interface{ Close() error }, proc *mcpProc, name string, budget time.Duration) {
	done := make(chan error, 1)
	go func() { done <- c.Close() }()
	logResult := func(err error) {
		if err != nil {
			logging.Debug("Error closing MCP client", "server", name, "cause", err)
		}
	}
	if proc != nil {
		for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL} {
			select {
			case err := <-done:
				logResult(err)
				return
			case <-time.After(mcpCloseGrace):
			}
			logging.Debug("MCP server still running after close; signalling its process group",
				"server", name, "signal", sig.String())
			proc.signal(sig)
		}
	}
	select {
	case err := <-done:
		logResult(err)
	case <-time.After(budget):
		logging.Warn("MCP client close exceeded its budget; abandoning the wait",
			"server", name, "budget", budget)
	}
}

// mcpConnBroken reports whether err means the client can no longer be
// trusted: any transport-level failure, the per-call timeout included. A
// JSON-RPC error the server answered with leaves the client healthy, and so
// does the caller's own context ending.
func mcpConnBroken(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	var te *transport.Error
	return errors.As(err, &te)
}

// mcpRequestNotSent reports whether err proves the request never reached the
// server, so sending it again on a fresh client cannot run it twice: the
// server dropped the session (HTTP 404), or the stdio server had exited
// before the request was written to it.
func mcpRequestNotSent(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, transport.ErrSessionTerminated) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, os.ErrClosed)
}
