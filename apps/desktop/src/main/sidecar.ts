import { spawn, ChildProcess } from 'node:child_process';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { freePort } from './freeport';
import { sidecarBinary, staticDir, dbPath } from './paths';
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

// startSidecar spawns the Go backend in --no-browser mode and resolves once
// it reports listening. Rejects if the process exits first or the timeout
// elapses. The returned stop() terminates the child.
export async function startSidecar(opts: StartOptions = {}): Promise<Sidecar> {
  const bin = sidecarBinary();
  if (!fs.existsSync(bin)) {
    throw new Error(`sidecar binary not found at ${bin} (set GRIDWELL_SIDECAR)`);
  }
  const port = opts.port ?? (await freePort());
  const db = dbPath();
  fs.mkdirSync(path.dirname(db), { recursive: true });

  const onLog = opts.onLog ?? ((l: string) => console.log('[sidecar]', l));
  const args = [
    'serve',
    '--no-browser',
    '--bind', `127.0.0.1:${port}`,
    '--db', db,
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
