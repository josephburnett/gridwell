import { spawn, ChildProcess } from 'node:child_process';
import * as fs from 'node:fs';
import { freePort } from './freeport';
import { sidecarBinary, staticDir } from './paths';
import { dialAddr, makeLineSplitter, parseServingLine, windowOrigin } from './lines';

export interface Sidecar {
  // The announced port and window origin, read back from the serve banner —
  // loopback by default, but server.yaml `web.bind` may pin another address
  // (e.g. a Tailscale IP shared with a phone browser).
  port: number;
  origin: string;
  // dialAddr is the gRPC node-export target: the federation socket from
  // the banner (unix:<path>) — a 0600 socket since 2026-08-26, so a window
  // on a Tailscale origin still dials its shells locally.
  dialAddr: string;
  // The web-UI auth token from the banner (the door is always gated; the
  // password is the minted web-password file — lines.ts owns the contract).
  // index.ts pre-sets it as the auth cookie so this window never prompts —
  // the password gate is for OTHER browsers reaching the shared origin.
  auth?: string;
  // external: the server was ALREADY RUNNING (someone else holds the home's
  // serve lock — internal/cli/servelock.go) and this app merely connected
  // to it. child is the exited probe process, not the server: never watch
  // it, never kill anything on stop.
  external: boolean;
  child: ChildProcess;
  stop: () => void;
}

export interface StartOptions {
  // Override the default bind port (otherwise a free ephemeral port is
  // chosen). Passed as --bind-default: an explicit `web.bind` in server.yaml
  // still wins — the server owns the listen-address decision.
  port?: number;
  // Milliseconds to wait for the ready line before giving up.
  timeoutMs?: number;
  // Sink for sidecar stdout/stderr lines (defaults to console).
  onLog?: (line: string) => void;
  // noServer: never START a server — run `gridwell status` instead of
  // `gridwell serve` and connect to a separately-run server (the advanced
  // split: `gridwell serve` in a terminal, the app with --no-server).
  // Rejects with a clear message when nothing is running.
  noServer?: boolean;
  // Test seams (sidecar.test.ts): a fake child process + fixed paths, so the
  // spawn/ready/error/exit/timeout settle rules — the logic that decides
  // whether the app boots or hangs — run under `node --test` with no binary
  // and no Electron. Production callers leave these unset.
  spawnFn?: (bin: string, args: string[]) => ChildProcess;
  binaryPath?: string;
  staticPath?: string;
  // initRetried is internal recursion state: the one no-config → `gridwell
  // init` → respawn pass has already happened, so a second no-config means
  // init did not take (surface it, don't loop).
  initRetried?: boolean;
}

// startSidecar spawns the Go backend (Connect-RPC + static files) and resolves once it
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
  // ~/.gridwell). A missing server.yaml is healed below: the one no-config
  // retry runs `gridwell init --kind local --name home` — first-run
  // friendliness in the app, while the server keeps its strict contract.
  // --bind-default (not --bind): the ephemeral loopback port applies only when
  // ~/.gridwell/server.yaml declares no web.bind of its own. A declared one
  // wins, so one server instance serves both this window and a phone browser
  // on a stable origin. The actual address comes back in the serve banner.
  //
  // noServer runs `gridwell status` instead: it never starts anything, only
  // re-emits a running server's banner ("already serving on ...") — the
  // server owns lock, discovery, and home resolution; this process never
  // learns what a home is.
  const args = opts.noServer
    ? ['status']
    : [
        'serve',
        '--bind-default', `127.0.0.1:${port}`,
        // --static only when explicitly overridden: the binary embeds the
        // web client (web/embed.go), and the dev tree / e2e harness still
        // pin their checkout via GRIDWELL_STATIC.
        ...staticArgs(opts.staticPath ?? envStaticDir()),
      ];
  const child = opts.spawnFn
    ? opts.spawnFn(bin, args)
    : spawn(bin, args, { stdio: ['ignore', 'pipe', 'pipe'] });

  const stop = () => {
    if (!child.killed) child.kill('SIGTERM');
  };

  // The one no-config heal: seen on stderr, remembered until the child
  // exits, then `gridwell init` and a single respawn.
  let sawNoConfig = false;
  // The server prints its actionable diagnostics ("no database at …",
  // "plugin binary not found") to stderr/stdout before exiting; keep the
  // tail so a boot failure dialog says WHY, not just an exit code.
  const lastLines: string[] = [];

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
      lastLines.push(line);
      if (lastLines.length > 8) lastLines.shift();
      if (/\bno config at /.test(line)) sawNoConfig = true;
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
          port: served.port,
          origin: windowOrigin(served),
          dialAddr: dialAddr(served),
          auth: served.auth,
          external: !!served.external,
          child,
          stop: served.external ? () => {} : stop,
        });
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
      if (sawNoConfig && !opts.noServer && !opts.initRetried) {
        // First run: no server.yaml yet. Create the default home plugin the
        // same way a user would, then start over — ONCE (a second no-config
        // means init itself failed; that error must surface, not loop).
        resolve(initThenRetry(bin, opts, onLog));
        return;
      }
      const tail = lastLines.length ? `\n${lastLines.join('\n')}` : '';
      reject(new Error(`sidecar exited before ready (code=${code} signal=${signal})${tail}`));
    });
  });
}

// initThenRetry runs `gridwell init --kind local --name home` (the app's
// first-run heal) and restarts the boot with the retry latch set.
async function initThenRetry(
  bin: string,
  opts: StartOptions,
  onLog: (line: string) => void,
): Promise<Sidecar> {
  onLog('[first run] no config — running gridwell init --kind local --name home');
  const initArgs = ['init', '--kind', 'local', '--name', 'home'];
  await new Promise<void>((resolve, reject) => {
    const child = opts.spawnFn
      ? opts.spawnFn(bin, initArgs)
      : spawn(bin, initArgs, { stdio: ['ignore', 'pipe', 'pipe'] });
    attachLineReader(child.stdout, onLog);
    attachLineReader(child.stderr, onLog);
    child.once('error', reject);
    child.once('exit', (code) =>
      code === 0 ? resolve() : reject(new Error(`gridwell init failed (code=${code})`)),
    );
  });
  return startSidecar({ ...opts, initRetried: true });
}

// staticArgs maps a static override to serve flags: none means the server
// serves its EMBEDDED web client (the packaged default).
function staticArgs(dir: string | undefined): string[] {
  return dir ? ['--static', dir] : [];
}

// envStaticDir is the dev/e2e override only — the packaged app passes
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
