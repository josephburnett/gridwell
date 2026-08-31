import { spawn, ChildProcess } from 'node:child_process';
import * as fs from 'node:fs';
import { freePort } from './freeport';
import { sidecarBinary, staticDir } from './paths';
import { makeLineSplitter, parseServingLine, windowOrigin } from './lines';

export interface Sidecar {
  // The window origin, read back from the serve banner. Loopback by default,
  // but server.yaml `web.bind` may pin another address, such as a Tailscale IP
  // shared with a phone browser.
  origin: string;
  // The web auth token from the banner. The door is always gated by the minted
  // web-password file; lines.ts owns the banner contract. index.ts pre-sets
  // this as the auth cookie so this window never prompts, since the gate is for
  // other browsers reaching the shared origin.
  auth?: string;
  // external: the server was already running (another process holds the home's
  // serve lock, internal/cli/servelock.go) and this app only connected to it.
  // child is then the exited probe process, not the server: never watch it, and
  // kill nothing on stop.
  external: boolean;
  child: ChildProcess;
  stop: () => void;
}

interface StartOptions {
  // Override the default bind port; otherwise a free ephemeral port is chosen.
  // Passed as --bind-default, so an explicit `web.bind` in server.yaml still
  // wins: the server owns the listen-address decision.
  port?: number;
  // Milliseconds of SILENCE to tolerate before giving up. It is not a
  // deadline on boot: every line the sidecar prints resets it, so a slow but
  // talking process — a one-database conversion, a migration chain over real
  // data — is never killed for taking its time. Only a process that has gone
  // quiet is.
  silenceMs?: number;
  // Sink for sidecar stdout/stderr lines (defaults to console).
  onLog?: (line: string) => void;
  // noServer: never start a server. Runs `gridwell status` instead of
  // `gridwell serve` and connects to a separately-run one, for the split where
  // `gridwell serve` runs in a terminal and the app is launched with
  // --no-server. Rejects with a clear message when nothing is running.
  noServer?: boolean;
  // Test seams (sidecar.test.ts): a fake child process and fixed paths, so the
  // spawn, ready, error, exit, and timeout settle rules run under `node --test`
  // with no binary and no Electron. Those rules decide whether the app boots or
  // hangs. Production callers leave these unset.
  spawnFn?: (bin: string, args: string[]) => ChildProcess;
  binaryPath?: string;
  staticPath?: string;
}

// startSidecar spawns the Go backend (Connect-RPC plus static files) and
// resolves once it announces its actual bound address ("gridwell: serving on
// ..."). It rejects if the process exits first or goes silent. The returned
// stop() terminates the child.
//
// Silent, not slow: the wait is a silence window reset by every line the
// sidecar prints, never a deadline on boot. A fixed deadline SIGTERMed a
// live, working server — an upgrade converting a real home announces each
// step it finishes and then takes minutes over the next one — and killing a
// mid-conversion process is how an upgrade path gets torn in half.
export async function startSidecar(opts: StartOptions = {}): Promise<Sidecar> {
  const bin = opts.binaryPath ?? sidecarBinary();
  if (!opts.spawnFn && !fs.existsSync(bin)) {
    throw new Error(`sidecar binary not found at ${bin} (set GRIDWELL_SIDECAR)`);
  }
  const port = opts.port ?? (await freePort());

  const onLog = opts.onLog ?? ((l: string) => console.log('[sidecar]', l));
  // No --db: the server resolves its own database under the Gridwell home
  // (GRIDWELL_HOME, inherited from this process's env, else ~/.gridwell), and
  // mints a missing server.yaml itself.
  // --bind-default, not --bind: the ephemeral loopback port applies only when
  // server.yaml declares no web.bind of its own. A declared one wins, so one
  // server serves both this window and a phone browser on a stable origin. The
  // actual address comes back in the serve banner.
  //
  // noServer runs `gridwell status` instead. It starts nothing and only
  // re-emits a running server's banner ("already serving on ..."). The server
  // owns the lock, discovery, and home resolution; this process never learns
  // what a home is.
  const args = opts.noServer
    ? ['status']
    : [
        'serve',
        '--bind-default', `127.0.0.1:${port}`,
        // --static only when explicitly overridden: the binary embeds the web
        // client (web/embed.go), and the dev tree and e2e harness pin their
        // checkout through GRIDWELL_STATIC.
        ...staticArgs(opts.staticPath ?? envStaticDir()),
      ];
  const child = opts.spawnFn
    ? opts.spawnFn(bin, args)
    : spawn(bin, args, { stdio: ['ignore', 'pipe', 'pipe'] });

  const stop = () => {
    if (!child.killed) child.kill('SIGTERM');
  };

  // The server prints its actionable diagnostics ("no database at …", "plugin
  // binary not found") to stdout or stderr before exiting. Keeping the tail
  // lets the boot failure dialog say why, not just an exit code.
  const lastLines: string[] = [];

  return await new Promise<Sidecar>((resolve, reject) => {
    let settled = false;
    const silenceMs = opts.silenceMs ?? 10_000;
    // One timer, re-armed on every line: the deadline is always "silenceMs
    // from the last thing it said", so progress buys time and only a hang
    // runs it out.
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

    // A spawn failure emits 'error' on the child instead of 'exit': the binary
    // is not executable, or the fs.existsSync check above raced a removal.
    // Without this listener boot would hang until the ready-timeout fired with
    // a generic "did not report ready"; reject with the real cause instead.
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

// staticArgs maps a static override to serve flags. None means the server
// serves its embedded web client, which is the packaged default.
function staticArgs(dir: string | undefined): string[] {
  return dir ? ['--static', dir] : [];
}

// envStaticDir is the dev and e2e override only. The packaged app passes
// nothing and the embedded client serves.
function envStaticDir(): string | undefined {
  return staticDir() ?? undefined;
}

// attachLineReader wires a stream to the pure line splitter.
function attachLineReader(stream: NodeJS.ReadableStream | null, cb: (line: string) => void): void {
  if (!stream) return;
  const splitter = makeLineSplitter(cb);
  stream.setEncoding('utf8');
  stream.on('data', (chunk: string) => splitter.push(chunk));
  stream.on('end', () => splitter.flush());
}
