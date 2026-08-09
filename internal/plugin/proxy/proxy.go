// Package proxy is a transparent Gridwell node: a GridwellServer that forwards
// every RPC — unary, server-stream, client-stream, and the bidirectional
// OpenShell — to a remote GridwellClient with full fidelity.
//
// TEST HARNESS ONLY since the #251 removal of the config-pinned ssh mode
// (its one production consumer): the internal/server transit seam tests wrap
// an in-process remote in one of these to exercise the server's chain
// qualification without standing up sshhost + an sshd. Production forwarding
// through a connection lives in sshhost's own per-connection routing.
package proxy

import (
	"context"
	"errors"
	"io"

	"google.golang.org/grpc"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// Plugin forwards the Gridwell service to a remote client.
type Plugin struct {
	gridwellv1.UnimplementedGridwellServer
	c gridwellv1.GridwellClient
}

// New wraps a remote client as a transparent GridwellServer.
func New(c gridwellv1.GridwellClient) *Plugin { return &Plugin{c: c} }

// ── unary forwards ───────────────────────────────────────────────────────────

func (p *Plugin) Info(ctx context.Context, r *gridwellv1.InfoRequest) (*gridwellv1.InfoResponse, error) {
	return p.c.Info(ctx, r)
}
func (p *Plugin) Probe(ctx context.Context, r *gridwellv1.ProbeRequest) (*gridwellv1.ProbeResponse, error) {
	return p.c.Probe(ctx, r)
}
func (p *Plugin) ListPlugins(ctx context.Context, r *gridwellv1.ListPluginsRequest) (*gridwellv1.ListPluginsResponse, error) {
	return p.c.ListPlugins(ctx, r)
}
func (p *Plugin) GetGrid(ctx context.Context, r *gridwellv1.GetGridRequest) (*gridwellv1.GetGridResponse, error) {
	return p.c.GetGrid(ctx, r)
}
func (p *Plugin) GetTile(ctx context.Context, r *gridwellv1.GetTileRequest) (*gridwellv1.TileResponse, error) {
	return p.c.GetTile(ctx, r)
}
func (p *Plugin) GetTilePreview(ctx context.Context, r *gridwellv1.GetTilePreviewRequest) (*gridwellv1.GetTilePreviewResponse, error) {
	return p.c.GetTilePreview(ctx, r)
}
func (p *Plugin) CreateTile(ctx context.Context, r *gridwellv1.CreateTileRequest) (*gridwellv1.TileResponse, error) {
	return p.c.CreateTile(ctx, r)
}
func (p *Plugin) SetTile(ctx context.Context, r *gridwellv1.SetTileRequest) (*gridwellv1.TileResponse, error) {
	return p.c.SetTile(ctx, r)
}
func (p *Plugin) CloneTile(ctx context.Context, r *gridwellv1.CloneTileRequest) (*gridwellv1.TileResponse, error) {
	return p.c.CloneTile(ctx, r)
}
func (p *Plugin) PlaceTile(ctx context.Context, r *gridwellv1.PlaceTileRequest) (*gridwellv1.TileResponse, error) {
	return p.c.PlaceTile(ctx, r)
}
func (p *Plugin) DeleteTile(ctx context.Context, r *gridwellv1.DeleteTileRequest) (*gridwellv1.DeleteTileResponse, error) {
	return p.c.DeleteTile(ctx, r)
}

// ── server-stream forwards (downstream only) ─────────────────────────────────

func (p *Plugin) ReadContent(r *gridwellv1.ReadContentRequest, ss grpc.ServerStreamingServer[gridwellv1.ContentChunk]) error {
	cs, err := p.c.ReadContent(ss.Context(), r)
	if err != nil {
		return err
	}
	return relay(cs, ss)
}

func (p *Plugin) Subscribe(r *gridwellv1.SubscribeRequest, ss grpc.ServerStreamingServer[gridwellv1.Event]) error {
	cs, err := p.c.Subscribe(ss.Context(), r)
	if err != nil {
		return err
	}
	return relay(cs, ss)
}

// recvStream / sendStream are the minimal halves of a one-way relay.
type recvStream[T any] interface{ Recv() (*T, error) }
type sendStream[T any] interface{ Send(*T) error }

func relay[T any](from recvStream[T], to sendStream[T]) error {
	for {
		msg, err := from.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := to.Send(msg); err != nil {
			return err
		}
	}
}

// ── client-stream forward (upstream then one response) ───────────────────────

// WriteContent forwards the content write with the same commit-at-close
// contract it carries everywhere: bytes relay upstream as they arrive, and
// the remote commits only when the local close propagates.
func (p *Plugin) WriteContent(ss grpc.ClientStreamingServer[gridwellv1.WriteContentRequest, gridwellv1.TileResponse]) error {
	cs, err := p.c.WriteContent(ss.Context())
	if err != nil {
		return err
	}
	for {
		msg, err := ss.Recv()
		if errors.Is(err, io.EOF) {
			resp, err := cs.CloseAndRecv()
			if err != nil {
				return err
			}
			return ss.SendAndClose(resp)
		}
		if err != nil {
			return err
		}
		if err := cs.Send(msg); err != nil {
			return err
		}
	}
}

// ── bidi forward (OpenShell) ─────────────────────────────────────────────────

func (p *Plugin) OpenShell(ss grpc.BidiStreamingServer[gridwellv1.OpenShellRequest, gridwellv1.OpenShellResponse]) error {
	cs, err := p.c.OpenShell(ss.Context())
	if err != nil {
		return err
	}
	errc := make(chan error, 2)
	// Upstream: keystrokes/resizes from the local side to the remote PTY.
	go func() {
		for {
			msg, err := ss.Recv()
			if err != nil {
				_ = cs.CloseSend()
				errc <- err
				return
			}
			if err := cs.Send(msg); err != nil {
				errc <- err
				return
			}
		}
	}()
	// Downstream: remote PTY output back to the local side.
	go func() {
		for {
			msg, err := cs.Recv()
			if err != nil {
				errc <- err
				return
			}
			if err := ss.Send(msg); err != nil {
				errc <- err
				return
			}
		}
	}()
	if err := <-errc; err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
