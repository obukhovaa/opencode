package agent

import (
	"fmt"

	"github.com/opencode-ai/opencode/internal/config"
)

// ReconnectMCP retires connections; existing users finish before close.
func ReconnectMCP(reg MCPRegistry, name string) error {
	r, ok := reg.(*mcpRegistry)
	if !ok {
		return fmt.Errorf("registry does not support reconnect")
	}
	cfg, ok := config.ResolveMCPServers()[name]
	if !ok {
		return fmt.Errorf("unknown MCP server %q", name)
	}
	p := r.pool
	p.mu.Lock()
	var closing []*mcpConn
	for key, conn := range p.conns {
		if conn.server != name {
			continue
		}
		delete(p.conns, key)
		conn.retired = true
		if conn.refs == 0 && !conn.closing {
			p.markClosingLocked(conn)
			closing = append(closing, conn)
		}
	}
	p.mu.Unlock()
	for _, conn := range closing {
		p.closeConn(conn, defaultCloseTiming())
	}
	r.mcpTools.Delete(name)
	r.getTools(name, cfg)
	if !r.LoadedServers()[name] {
		return fmt.Errorf("MCP server %q could not connect", name)
	}
	return nil
}
