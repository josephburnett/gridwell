package server_test

// The disable_shells contract (server.yaml), pinned over the REAL wire —
// the gRPC node export behind FederationHandler, what a remote mounter
// speaks. (The BROWSER door's half — the /shell WebSocket — is pinned by
// shell_door_seam_test.go; both enter through the one shell route, so the
// refusal cannot hold on one door and not the other.) The refusal lives at
// the node's router, before plugin resolution, so no plugin (local or
// mounted) can serve a shell while the flag is set:
//   - Handshake carries shells_disabled (the client drops the palette
//     swatch from it),
//   - CreateTile(kind=shell) is PermissionDenied while other kinds work,
//   - OpenShell is PermissionDenied — the only PTY door on the node,
//   - ShellSessionAlive answers "gone", so a pre-existing shell tile's
//     refresh affordance hides exactly like a dead tmux session's.

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/server"
)

// rootGrid resolves the registered localdb's root grid through the
// handshake, like every other export test.
func rootGrid(t *testing.T, c gridwellv1.GridwellClient) string {
	return homeRoot(t, c)
}

func TestShellsDisabled(t *testing.T) {
	c, direct := nodeServerCfg(t, server.Config{DisableShells: true})
	ctx := context.Background()

	// The handshake tells the client.
	lp, err := c.Handshake(ctx, &gridwellv1.HandshakeRequest{})
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	if !lp.ShellsDisabled {
		t.Errorf("Handshake.ShellsDisabled = false, want true")
	}

	grid := rootGrid(t, c)

	// Creating a shell is refused at the router; a text tile on the same
	// grid still works (the gate is kind-scoped, not a write lock).
	_, err = c.CreateTile(ctx, &gridwellv1.CreateTileRequest{
		GridId: grid,
		Tile:   &gridwellv1.Tile{Kind: "shell", X: 1, Y: 1, W: 1, H: 1},
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("CreateTile(shell) = %v, want PermissionDenied", err)
	}
	if _, err := c.CreateTile(ctx, &gridwellv1.CreateTileRequest{
		GridId: grid,
		Tile:   &gridwellv1.Tile{Kind: "text", X: 2, Y: 1, W: 1, H: 1},
	}); err != nil {
		t.Errorf("CreateTile(text) must still work: %v", err)
	}

	// A shell tile that PRE-DATES the flag (created through the plugin
	// directly — placement is sacred, the row stays): its PTY door is
	// closed and its session reads as gone.
	pre, err := direct.CreateTile(ctx, &gridwellv1.CreateTileRequest{
		GridId: gridLocal(grid),
		Tile:   &gridwellv1.Tile{Kind: "shell", X: 3, Y: 1, W: 1, H: 1},
	})
	if err != nil {
		t.Fatalf("pre-existing shell (direct): %v", err)
	}
	qualified := "ur1/" + pre.Tile.Id

	stream, err := c.OpenShell(ctx)
	if err != nil {
		t.Fatalf("OpenShell dial: %v", err)
	}
	_ = stream.Send(&gridwellv1.OpenShellRequest{TileId: qualified, Resize: &gridwellv1.PTYSize{Cols: 80, Rows: 24}})
	if _, err := stream.Recv(); status.Code(err) != codes.PermissionDenied {
		t.Errorf("OpenShell = %v, want PermissionDenied", err)
	}

	alive, err := c.ShellSessionAlive(ctx, &gridwellv1.ShellSessionAliveRequest{TileId: qualified})
	if err != nil {
		t.Fatalf("ShellSessionAlive: %v", err)
	}
	if alive.Alive {
		t.Errorf("ShellSessionAlive = true, want false (unreachable by design)")
	}
}

// TestShellsEnabledByDefault pins the regression direction: the flag off is
// today's behavior — the handshake says so and shells create normally.
func TestShellsEnabledByDefault(t *testing.T) {
	c, _ := nodeServer(t)
	ctx := context.Background()
	lp, err := c.Handshake(ctx, &gridwellv1.HandshakeRequest{})
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	if lp.ShellsDisabled {
		t.Errorf("Handshake.ShellsDisabled = true with no flag set")
	}
	if _, err := c.CreateTile(ctx, &gridwellv1.CreateTileRequest{
		GridId: rootGrid(t, c),
		Tile:   &gridwellv1.Tile{Kind: "shell", X: 1, Y: 1, W: 1, H: 1},
	}); err != nil {
		t.Errorf("CreateTile(shell) must work by default: %v", err)
	}
}

// gridLocal strips the plugin qualifier for a DIRECT (in-plugin) call.
func gridLocal(qualified string) string {
	for i := 0; i < len(qualified); i++ {
		if qualified[i] == '/' {
			return qualified[i+1:]
		}
	}
	return qualified
}
