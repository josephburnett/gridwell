package namespace

// Server writes a Namespace onto the gridwell.v1 gRPC service, for
// internal/server's ConnectionHandler, which serves the node's router to a
// connecting node. That export is the connection API, never a second router.

import pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"

// Server adapts a Namespace to the gridwell.v1 gRPC service. Every unary verb
// has the same signature on both, so the embedded Namespace answers it; the
// stream verbs below translate their shapes.
func Server(ns Namespace) pb.GridwellServer { return &grpcServer{Namespace: ns} }

type grpcServer struct {
	Namespace
	unimplemented
}

// unimplemented sits a level below Namespace, so its methods lose to the
// Namespace's and only gRPC's embedding guard reaches grpcServer from it.
type unimplemented struct{ pb.UnimplementedGridwellServer }

func (g *grpcServer) ReadContent(req *pb.ReadContentRequest, stream pb.Gridwell_ReadContentServer) error {
	return g.Namespace.ReadContent(stream.Context(), req, stream.Send)
}

func (g *grpcServer) ServeContent(req *pb.ServeContentRequest, stream pb.Gridwell_ServeContentServer) error {
	return g.Namespace.ServeContent(stream.Context(), req, stream.Send)
}

func (g *grpcServer) Subscribe(req *pb.SubscribeRequest, stream pb.Gridwell_SubscribeServer) error {
	sent := false
	return g.Namespace.Subscribe(stream.Context(), req, func(ev *pb.Event) error {
		if ev == nil && sent {
			// The first event already carried the header.
			return nil
		}
		sent = true
		if ev == nil {
			return stream.SendHeader(nil)
		}
		return stream.Send(ev)
	})
}

func (g *grpcServer) WriteContent(stream pb.Gridwell_WriteContentServer) error {
	resp, err := g.Namespace.WriteContent(stream.Context(), stream.Recv)
	if err != nil {
		return err
	}
	return stream.SendAndClose(resp)
}

func (g *grpcServer) OpenShell(stream pb.Gridwell_OpenShellServer) error {
	return g.Namespace.OpenShell(stream.Context(), stream.Recv, stream.Send)
}
