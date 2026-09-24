import { spawn, ChildProcess } from 'node:child_process';
import * as fs from 'node:fs';
import { freePort } from './freeport';
import { sidecarBinary, staticDir } from './paths';
import { makeLineSplitter, parseServingLine, windowOrigin } from './lines';
import { trace } from './trace';

export interface Sidecar {
  // From the serve banner: loopback, unless server.yaml `web.bind` pins one.
  origin: string;
  // From the banner too; lines.ts owns the banner contract.
  auth?: string;
  // Another process holds the home's serve lock (internal/cli/servelock.go), so
  // child is the exited probe: never watch it, and kill nothing on stop.
  external: boolean;
  child: ChildProcess;
  stop: () => void;
}

interface StartOptions {
  // Passed as --bind-default, so an explicit `web.bind` still wins.
  port?: number;
  // Milliseconds of silence to tolerate before giving up.
  silenceMs?: number;
  // Sink for sidecar stdout/stderr lines (defaults to console).
  onLog?: (line: string) => void;
  // Run `gridwell status` and connect to a separately-run server.
  noServer?: boolean;
  // Test seams, so the settle rules run with no binary and no Electron.
  spawnFn?: (bin: string, args: string[]) => ChildProcess;
  binaryPath?: string;
  staticPath?: string;
}

// Every sidecar-lifecycle record wears this src.
const TRACE_SRC = 'sidecar';

// startSidecar resolves once the serve banner announces the bound address. The
// wait bounds silence rather than total time, because a fixed deadline SIGTERMs
// a live working server, and killing one mid-write tears a home in half.
export async function startSidecar(opts: StartOptions = {}): Promise<Sidecar> {
  const bin = opts.binaryPath ?? sidecarBinary();
  if (!opts.spawnFn && !fs.existsSync(bin)) {
    throw new Error(`sidecar binary not found at ${bin} (set GRIDWELL_SIDECAR)`);
  }
  const port = opts.port ?? (await freePort());

  const onLog = opts.onLog ?? ((l: string) => console.log('[sidecar]', l));
  // No --db: the server resolves its own database under the Gridwell home and
  // mints a missing server.yaml. --bind-default applies only when server.yaml
  // declares no web.bind. `status` starts nothing and re-emits a running
  // server's banner. This process never learns what a home is.
  const args = opts.noServer
    ? ['status']
    : [
        'serve',
        '--bind-default', `127.0.0.1:${port}`,
        // --static only when overridden; the binary embeds the web client.
        ...staticArgs(opts.staticPath ?? envStaticDir()),
      ];
  const child = opts.spawnFn
    ? opts.spawnFn(bin, args)
    : spawn(bin, args, { stdio: ['ignore', 'pipe', 'pipe'] });
  trace({ src: TRACE_SRC, kind: 'spawn', msg: [bin, ...args].join(' ') });
  // The one place an exit is traced, whenever it happens: the settle listeners
  // below stop at boot, and index.ts's watcher only surfaces the notice. The
  // sidecar's own stdout is already in the node's ring through its log capture,
  // so onLog posts nothing.
  child.on('exit', (code, signal) =>
    trace({
      src: TRACE_SRC,
      kind: 'exit',
      msg: 'sidecar exited',
      kv: { code: String(code ?? ''), signal: signal ?? '' },
    }),
  );

  const stop = () => {
    if (!child.killed) child.kill('SIGTERM');
  };

  // Keeping the tail lets the boot-failure dialog say why, not just the code.
  const lastLines: string[] = [];

  return await new Promise<Sidecar>((resolve, reject) => {
    let settled = false;
    const silenceMs = opts.silenceMs ?? 10_000;
    // Re-armed on every line, so the deadline is always silenceMs from the
    // last thing the sidecar said.
    const arm = () =>
      setTimeout(() => {
        if (settled) return;
        settled = true;
        stop();
        reject(new Error(`sidecar went silent for ${silenceMs}ms without reporting ready`));
      }, silenceMs);
    let timer = arm();

    const handleLine = (line: string) => {
      onLog(line);
      if (settled) return;
      clearTimeout(timer);
      timer = arm();
      lastLines.push(line);
      if (lastLines.length > 8) lastLines.shift();
      if (opts.noServer && /^gridwell: not serving\b/.test(line)) {
        settled = true;
        clearTimeout(timer);
        reject(
          new Error(
            'no server is running (--no-server given): start one with `gridwell serve`, then relaunch',
          ),
        );
        return;
      }
      const served = parseServingLine(line);
      if (served) {
        settled = true;
        clearTimeout(timer);
        trace({
          src: TRACE_SRC,
          kind: 'ready',
          msg: windowOrigin(served),
          kv: { external: String(!!served.external) },
        });
        resolve({
          origin: windowOrigin(served),
          auth: served.auth,
          external: !!served.external,
          child,
          stop: served.external ? () => {} : stop,
        });
      }
    };

    attachLineReader(child.stdout, handleLine);
    attachLineReader(child.stderr, handleLine);

    // A spawn failure emits 'error', not 'exit'. Without this, boot hangs to
    // the silence timer and reports a generic message.
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
      const tail = lastLines.length ? `\n${lastLines.join('\n')}` : '';
      reject(new Error(`sidecar exited before ready (code=${code} signal=${signal})${tail}`));
    });
  });
}

// No override means the server's embedded web client.
function staticArgs(dir: string | undefined): string[] {
  return dir ? ['--static', dir] : [];
}

function envStaticDir(): string | undefined {
  return staticDir() ?? undefined;
}

function attachLineReader(stream: NodeJS.ReadableStream | null, cb: (line: string) => void): void {
  if (!stream) return;
  const splitter = makeLineSplitter(cb);
  stream.setEncoding('utf8');
  stream.on('data', (chunk: string) => splitter.push(chunk));
  stream.on('end', () => splitter.flush());
}
