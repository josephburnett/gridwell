import { spawn, ChildProcess } from 'node:child_process';
import * as fs from 'node:fs';
import { freePort } from './freeport';
import { sidecarBinary, staticDir } from './paths';
import { isReadyLine, makeLineSplitter } from './lines';

export interface Sidecar {
  port: number;
  origin: string; // http://127.0.0.1:<port>
  child: ChildProcess;
  stop: () => void;
}

export interface StartOptions {
  // Override the bind port (otherwise a free ephemeral port is chosen).
  port?: number;
  // Milliseconds to wait for the ready line before giving up.
  timeoutMs?: number;
  // Sink for sidecar stdout/stderr lines (defaults to console).
  onLog?: (line: string) => void;
}

// startSidecar spawns the Go backend (loopback HTTP/SSE/WS) and resolves once
// it reports listening. Rejects if the process exits first or the timeout
// elapses. The returned stop() terminates the child.
export async function startSidecar(opts: StartOptions = {}): Promise<Sidecar> {
  const bin = sidecarBinary();
  if (!fs.existsSync(bin)) {
    throw new Error(`sidecar binary not found at ${bin} (set GRIDWELL_SIDECAR)`);
  }
  const port = opts.port ?? (await freePort());

  const onLog = opts.onLog ?? ((l: string) => console.log('[sidecar]', l));
  // No --db: the server derives every plugin's DB path from its id under the
  // Gridwell home (GRIDWELL_HOME, inherited from this process's env, else
  // ~/.gridwell). It requires ~/.gridwell/server.yaml — run `gridwell init`
  // (or `make init`) once to create it; there is no fallback DB.
  const args = [
    'serve',
    '--bind', `127.0.0.1:${port}`,
    '--static', staticDir(),
  ];
  const child = spawn(bin, args, { stdio: ['ignore', 'pipe', 'pipe'] });

  const origin = `http://127.0.0.1:${port}`;
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
      if (!settled && isReadyLine(line)) {
        settled = true;
        clearTimeout(timer);
        resolve({ port, origin, child, stop });
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
