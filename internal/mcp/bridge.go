package mcp

import (
	"context"
	"fmt"
	"io"
	"net"
)

// Bridge is `agent-office mcp`: Claude Code starts it as a stdio MCP server,
// and it pipes stdin to the agent's MCP socket and the socket to stdout
// (ADR 0006). It returns when the daemon closes the connection, or after
// stdin ends and the daemon has finished answering.
func Bridge(ctx context.Context, socketPath string, in io.Reader, out io.Writer) error {
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return fmt.Errorf("connect to the agent-office daemon: %w", err)
	}
	defer c.Close()
	go func() {
		_, _ = io.Copy(c, in)
		if uc, ok := c.(*net.UnixConn); ok {
			_ = uc.CloseWrite() // the server sees EOF and ends the session
		}
	}()
	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(out, c)
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
