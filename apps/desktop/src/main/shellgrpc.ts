// The gRPC glue for the shell transport: dials the sidecar's node export
// (raw gRPC on the same h2c port every other caller uses) and opens the
// bidirectional OpenShell stream. The proto definition is loaded at runtime
// from the repo's single source (api/gridwell/v1/data.proto — see
// dataProtoPath in paths.ts); nothing here re-declares a message shape, so
// the wire cannot drift from the one .proto.
//
// Only glue lives here. The lifecycle rules (replace-on-open, exactly-once
// exit, no-op after close) are in shellstreams.ts, unit-tested with a fake
// dialer.
import * as grpc from '@grpc/grpc-js';
import * as protoLoader from '@grpc/proto-loader';
import { ShellDialer, ShellStreamHandle } from './shellstreams';

interface OpenShellRequest {
  tile_id?: string;
  data?: Buffer;
  resize?: { cols: number; rows: number };
}
interface OpenShellResponse {
  data: Buffer;
}

// sessionGoneCodes are the status codes the plugin uses for "this PTY session
// no longer exists" (as opposed to a transport failure): the renderer flips
// the refresh affordance off for these.
const sessionGoneCodes = new Set<number>([grpc.status.NOT_FOUND, grpc.status.FAILED_PRECONDITION]);

// makeShellDialer loads the proto and returns a ShellDialer bound to the
// sidecar's federation socket ("unix:<path>").
export function makeShellDialer(address: string, protoPath: string): ShellDialer {
  const def = protoLoader.loadSync(protoPath, {
    keepCase: true,
    longs: Number,
    defaults: true,
  });
  const pkg = grpc.loadPackageDefinition(def) as unknown as {
    gridwell: { v1: { Gridwell: grpc.ServiceClientConstructor } };
  };
  const client = new pkg.gridwell.v1.Gridwell(address, grpc.credentials.createInsecure());

  return (tileId, cols, rows, onData, onEnd): ShellStreamHandle => {
    const call = (
      client as unknown as {
        OpenShell(): grpc.ClientDuplexStream<OpenShellRequest, OpenShellResponse>;
      }
    ).OpenShell();

    call.on('data', (resp: OpenShellResponse) => {
      if (resp.data && resp.data.length > 0) onData(new Uint8Array(resp.data));
    });
    call.on('error', (err: grpc.ServiceError) => {
      onEnd(err.details || err.message, sessionGoneCodes.has(err.code));
    });
    call.on('end', () => onEnd('', false));

    // Bind message: tile id + initial size, no data.
    call.write({ tile_id: tileId, resize: { cols, rows } });

    return {
      write(data: Uint8Array): void {
        call.write({ data: Buffer.from(data) });
      },
      resize(c: number, r: number): void {
        call.write({ resize: { cols: c, rows: r } });
      },
      close(): void {
        call.end();
      },
    };
  };
}
