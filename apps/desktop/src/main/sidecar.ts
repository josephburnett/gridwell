import { spawn, ChildProcess } from 'node:child_process';
import * as fs from 'node:fs';
import { freePort } from './freeport';
import { sidecarBinary, staticDir } from './paths';
import { makeLineSplitter, parseServingLine, windowOrigin } from './lines';

export interface Sidecar {
  // The announced port and window origin, read back from the serve banner —
  // loopback by default, but server.yaml `bind:` may pin another address
  // (e.g. a Tailscale IP shared with a phone browser).
  port: number;
  origin: string;
  child: ChildProcess;
  stop: () => void;
}

export interface StartOptions {
  // Override the default bind port (otherwise a free ephemeral port is
  // chosen). Passed as --bind-default: an explicit `bind:` in server.yaml
  // still wins — the server owns the listen-address decision.
  port?: number;
  // Milliseconds to wait for the ready line before giving up.
  timeoutMs?: number;
  // Sink for sidecar stdout/stderr lines (defaults to console).
  onLog?: (line: string) => void;
  // Test seams (sidecar.test.ts): a fake child process + fixed paths, so the
  // spawn/ready/error/exit/timeout settle rules — the logic that decides
  // whether the app boots or hangs — run under `node --test` with no binary
  // and no Electron. Production callers leave these unset.
  spawnFn?: (bin: string, args: string[]) => ChildProcess;
  binaryPath?: string;
  staticPath?: string;
}

// startSidecar spawns the Go backend (HTTP/SSE/WS) and resolves once it
// announces its actual bound address ("gridwell: serving on ..."). Rejects if
// the process exits first or the timeout elapses. The returned stop()
// terminates the child.
export async function startSidecar(opts: StartOptions = {}): Promise<Sidecar> {
  const bin = opts.binaryPath ?? sidecarBinary();
  if (!opts.spawnFn && !fs.existsSync(bin)) {
    throw new Error(`sidecar binary not found at ${bin} (set GRIDWELL_SIDECAR)`);
  }
  const port = opts.port ?? (await freePort());

  const onLog = opts.onLog ?? ((l: string) => console.log('[sidecar]', l));
  // No --db: the server derives every plugin's DB path from its id under the
  // Gridwell home (GRIDWELL_HOME, inherited from this process's env, else
  // ~/.gridwell). It requires ~/.gridwell/server.yaml — run `gridwell init`
  // (or `make init`) once to create it; there is no fallback DB.
  // --bind-default (not --bind): the ephemeral loopback port applies only when
  // ~/.gridwell/server.yaml declares no bind: of its own. A declared bind:
  // wins, so one server instance serves both this window and a phone browser
  // on a stable origin. The actual address comes back in the serve banner.
  const args = [
    'serve',
    '--bind-default', `127.0.0.1:${port}`,
    '--static', opts.staticPath ?? staticDir(),
  ];
  const child = opts.spawnFn
    ? opts.spawnFn(bin, args)
    : spawn(bin, args, { stdio: ['ignore', 'pipe', 'pipe'] });

  const stop = () => {
    if (!child.killed) child.kill('SIGTERM');
  };

  return await new Promise<Sidecar>((resolve, reject) => {
    let settled = false;
    const timeoutMs = opts.timeoutMs ?? 10_000;
    const timer = setTimeout(() => {
      if (settled) return;
      settled = true;
      stop();
      reject(new Error(`sidecar did not report ready within ${timeoutMs}ms`));
    }, timeoutMs);

    const handleLine = (line: string) => {
      onLog(line);
      if (settled) return;
      const served = parseServingLine(line);
      if (served) {
        settled = true;
        clearTimeout(timer);
        resolve({ port: served.port, origin: windowOrigin(served), child, stop });
      }
    };

    attachLineReader(child.stdout, handleLine);
    attachLineReader(child.stderr, handleLine);

    // A spawn failure (e.g. the binary isn't executable, or the fs.existsSync
    // check above raced a removal) emits 'error' on the child instead of
    // 'exit' — before this listener existed nothing here reacted to it, so
    // boot just hung until the 10s ready-timeout fired with a generic
    // "did not report ready" message (issue #46 point 1). Reject immediately
    // with the real cause instead.
    child.once('error', (err) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      reject(err);
    });

    child.once('exit', (code, signal) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      reject(new Error(`sidecar exited before ready (code=${code} signal=${signal})`));
    });
  });
}

// attachLineReader wires a stream to the pure line splitter.
function attachLineReader(stream: NodeJS.ReadableStream | null, cb: (line: string) => void): void {
  if (!stream) return;
  const splitter = makeLineSplitter(cb);
  stream.setEncoding('utf8');
  stream.on('data', (chunk: string) => splitter.push(chunk));
  stream.on('end', () => splitter.flush());
}
