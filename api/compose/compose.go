package compose

// The Loadout: one constructor per plugin, chosen by the COMPOSER of a
// binary — in-process or out-of-process — and indistinguishable to every
// caller above (the same GridwellClient either way). Enumeration of what
// ships is a leaf-binary privilege (charter, 2026-08-15); this type is
// where that privilege is exercised.

import (
	"fmt"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// Factory constructs an in-process plugin from its config map — the SAME
// map a subprocess plugin reads from the spawn environment (guest.Config):
// db_file, uuid, kind, plus any plugin-specific keys. One config
// vocabulary, both process shapes.
type Factory func(cfg map[string]string) (gridwellv1.GridwellServer, error)

// Loadout names one way to materialize a plugin.
type Loadout struct {
	factory Factory
	command string
}

// InProcess is a compiled-in plugin: the factory runs in this process and
// serves over an in-memory loopback (a bundled binary; iOS, where
// fork/exec does not exist).
func InProcess(f Factory) Loadout { return Loadout{factory: f} }

// Command is an out-of-process plugin: the binary at path is spawned and
// handshaken via go-plugin — its own process, its own dependency graph
// (the third-party door).
func Command(path string) Loadout { return Loadout{command: path} }

// IsZero reports an unset Loadout.
func (l Loadout) IsZero() bool { return l.factory == nil && l.command == "" }

// Open materializes the plugin: spawn-and-handshake for a Command,
// construct-and-serve for an InProcess factory. Either way the caller
// holds a GridwellClient and a closer, and cannot tell which it got.
func (l Loadout) Open(cfg map[string]string) (gridwellv1.GridwellClient, func(), error) {
	switch {
	case l.command != "":
		return LoadPlugin(l.command, cfg)
	case l.factory != nil:
		impl, err := l.factory(cfg)
		if err != nil {
			return nil, nil, err
		}
		return ServeInProcess(impl)
	}
	return nil, nil, fmt.Errorf("compose: empty loadout")
}

// ServeInProcess starts a gRPC server in a goroutine on a loopback TCP
// port and returns a client connected to it. closer stops the server and
// closes the connection. The in-process half of the Loadout door; also
// the seam-test harness everywhere a real plugin is exercised without a
// subprocess.
func ServeInProcess(impl gridwellv1.GridwellServer) (gridwellv1.GridwellClient, func(), error) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, fmt.Errorf("in-process listen: %w", err)
	}

	srv := grpc.NewServer()
	gridwellv1.RegisterGridwellServer(srv, impl)
	go srv.Serve(lis)

	addr := lis.Addr().String()
	cc, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		srv.Stop()
		return nil, nil, fmt.Errorf("in-process dial %s: %w", addr, err)
	}

	closer := func() {
		cc.Close()
		srv.GracefulStop()
	}
	return gridwellv1.NewGridwellClient(cc), closer, nil
}
