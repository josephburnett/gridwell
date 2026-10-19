package server

// The MCP API: the third API that decodes onto the one router, at mcpapi.Path
// on the web API's gated mux, Streamable HTTP. An agent authenticates with the
// web password as a bearer (auth.go), and no build gate applies, since no page
// of this node's build is behind it; the tool list is its contract.

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/josephburnett/gridwell/internal/mcpapi"
	"github.com/josephburnett/gridwell/internal/trace"
)

// mcpAPI is stateless: each request stands alone, so the API holds no
// session for an agent that went away.
func (s *Server) mcpAPI() http.Handler {
	srv := mcpapi.New(newRouter(s), s.cfg.ID, s.build, s)
	srv.AddReceivingMiddleware(traceTools)
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
}

// traceTools gives each tool call the start and end records every rpc gets,
// verb "mcp/<tool>", naming the tile or grid its arguments name and the shell
// the agent runs in (mcpapi.ShellHeader, a tile's key, no secret). A list's
// verdicts are the reply, not an error, so each item gets its own record and
// the end record counts them.
func traceTools(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		call, ok := req.(*mcp.CallToolRequest)
		if !ok {
			return next(ctx, method, req)
		}
		tool := call.Params.Name
		sp := rpcSpan("", tool, nil)
		sp.verb, sp.id = "mcp/"+tool, tracedID(call.Params.Arguments)
		if call.Extra != nil {
			if shell := call.Extra.Header.Get(mcpapi.ShellHeader); shell != "" {
				sp.extra = map[string]string{"shell_key": shell}
			}
		}
		sp.open()
		res, err := next(ctx, method, req)
		traced, code := err, "protocol"
		if r, ok := res.(*mcp.CallToolResult); ok && err == nil {
			if r.IsError {
				traced, code = toolError(r), "refused"
			} else {
				traceItems(sp, tool, call.Params.Arguments, r.StructuredContent)
			}
		}
		sp.end(traced, code)
		return res, err
	}
}

// traceItems records each verdict of a list's reply against the id its item
// named, and puts the counts on the span's end record.
func traceItems(sp *span, tool string, args json.RawMessage, reply any) {
	raw, ok := reply.(json.RawMessage)
	if !ok {
		return
	}
	var out struct {
		Items, Tiles []struct{ Verdict string }
	}
	if json.Unmarshal(raw, &out) != nil {
		return
	}
	verdicts := append(out.Items, out.Tiles...)
	if len(verdicts) == 0 {
		return
	}
	var in struct {
		IDs   []string
		Items []struct{ ID, Grid string }
	}
	_ = json.Unmarshal(args, &in)
	refused := 0
	for i, v := range verdicts {
		var id string
		switch {
		case i < len(in.Items):
			id = cmp.Or(in.Items[i].ID, in.Items[i].Grid)
		case i < len(in.IDs):
			id = in.IDs[i]
		}
		if v.Verdict != "ok" {
			refused++
		}
		trace.Emit("mcp", "item", tool+" "+v.Verdict, map[string]string{
			"tool": tool, "i": strconv.Itoa(i), "id": id, "verdict": v.Verdict})
	}
	if sp.extra == nil {
		sp.extra = map[string]string{}
	}
	sp.extra["items"], sp.extra["refused"] = strconv.Itoa(len(verdicts)), strconv.Itoa(refused)
}

// tracedID is the tile or grid a call's arguments name first: its own, or
// the first item's or id's of a list.
func tracedID(raw json.RawMessage) string {
	type named struct{ ID, Grid string }
	var args struct {
		named
		IDs   []string
		Items []named
	}
	_ = json.Unmarshal(raw, &args)
	if len(args.Items) > 0 {
		args.named = args.Items[0]
	}
	if len(args.IDs) > 0 {
		args.ID = args.IDs[0]
	}
	return cmp.Or(args.ID, args.Grid)
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
