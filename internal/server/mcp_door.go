package server

// The MCP door: the third codec over the one router, at mcpdoor.Path on the
// web door's gated mux, Streamable HTTP. An agent authenticates with the web
// password as a bearer (auth.go), and no build gate applies, since no page
// of this node's build is behind it; the tool list is its contract.

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/josephburnett/gridwell/internal/mcpdoor"
)

// mcpDoor is stateless: each request stands alone, so the door holds no
// session for an agent that went away.
func (s *Server) mcpDoor() http.Handler {
	srv := mcpdoor.New(newRouter(s), s.cfg.ID, s.build, s)
	srv.AddReceivingMiddleware(traceTools)
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
}

// traceTools gives each tool call the start and end records every rpc gets,
// verb "mcp/<tool>", naming the tile or grid its arguments name.
func traceTools(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		call, ok := req.(*mcp.CallToolRequest)
		if !ok {
			return next(ctx, method, req)
		}
		sp := rpcSpan("", "mcp/"+call.Params.Name, nil)
		var args struct{ ID, Grid string }
		_ = json.Unmarshal(call.Params.Arguments, &args)
		sp.id = args.ID
		if sp.id == "" {
			sp.id = args.Grid
		}
		res, err := next(ctx, method, req)
		traced, code := err, "protocol"
		if r, ok := res.(*mcp.CallToolResult); ok && err == nil && r.IsError {
			traced, code = toolError(r), "refused"
		}
		sp.end(traced, code)
		return res, err
	}
}

type toolErr string

func (e toolErr) Error() string { return string(e) }

// toolError is a refused tool call's reason, for the trace.
func toolError(r *mcp.CallToolResult) error {
	for _, c := range r.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			return toolErr(t.Text)
		}
	}
	return toolErr("refused")
}
