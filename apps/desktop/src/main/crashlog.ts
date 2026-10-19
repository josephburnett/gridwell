// The crash file: what main writes to the node's dumps directory when the node
// or the root renderer dies, because the trace ring dies with the node and a
// panic's stack never reaches it. This owns the bounded tails of both
// processes' console lines, the redaction that keeps the web password and its
// token out of the file, and the file's name and text.

import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { logLine } from './trace';

// Lines kept per process: enough for a Go panic's stack, bounded so a chatty
// run costs nothing.
const CRASH_TAIL_LINES = 200;

// The node prints its web password three ways on start (internal/cli/serve.go:
// the banner's auth=, the "web password:" line, the MCP line's bearer), and
// any of them would hand a reader of the file the node.
const SECRETS: [RegExp, string][] = [
  [/\bauth=\S+/g, 'auth=<redacted>'],
  [/(web password:\s*)\S+/gi, '$1<redacted>'],
  [/(Bearer\s+)[^\s"']+/gi, '$1<redacted>'],
];

export function redact(line: string): string {
  let out = line;
  for (const [re, sub] of SECRETS) out = out.replace(re, sub);
  return out;
}

// LineTail keeps the last cap lines, redacted as they arrive, so no secret is
// held in memory either.
export class LineTail {
  private readonly cap: number;
  private buf: string[] = [];

  constructor(cap = CRASH_TAIL_LINES) {
    this.cap = cap;
  }

  push(line: string): void {
    this.buf.push(redact(line));
    if (this.buf.length > this.cap) this.buf.splice(0, this.buf.length - this.cap);
  }

  lines(): string[] {
    return [...this.buf];
  }
}

// The two tails of this run. sidecar.ts feeds the node's stdout and stderr,
// window.ts the root renderer's console.
export const sidecarTail = new LineTail();
export const rendererTail = new LineTail();

// The node's directory and its dumps under it: config.Dir and config.DumpsDir,
// which crashlog.test.ts pins. Main reads it only to write here.
export function dumpsDir(env: Record<string, string | undefined>, home: string): string {
  const dir = env['GRIDWELL_HOME'] || path.join(home, '.gridwell');
  return path.join(dir, 'dumps');
}

// Named like internal/trace.DumpName, so a crash sorts beside the dumps of the
// same moment.
export function crashFileName(now: Date): string {
  const p = (n: number) => String(n).padStart(2, '0');
  const d = `${now.getUTCFullYear()}${p(now.getUTCMonth() + 1)}${p(now.getUTCDate())}`;
  const t = `${p(now.getUTCHours())}${p(now.getUTCMinutes())}${p(now.getUTCSeconds())}`;
  return `crash-${d}-${t}.txt`;
}

interface CrashReport {
  // Which death: 'sidecar-exit' or 'renderer-gone'.
  cause: string;
  kv: Record<string, string>;
  when: Date;
  sidecar: string[];
  renderer: string[];
}

export function crashText(r: CrashReport): string {
  const out = [`gridwell desktop crash: ${r.cause}`, `when: ${r.when.toISOString()}`];
  for (const k of Object.keys(r.kv).sort()) out.push(`${k}: ${r.kv[k]}`);
  out.push('', `--- node output, last ${r.sidecar.length} lines ---`, ...r.sidecar);
  out.push('', `--- renderer console, last ${r.renderer.length} lines ---`, ...r.renderer, '');
  return out.join('\n');
}

// writeCrashFile writes both tails and answers the file's path. A failure
// throws; the caller logs it, since a crash file that could not be written is
// itself something to say.
function writeCrashFile(cause: string, kv: Record<string, string>, now = new Date()): string {
  const dir = dumpsDir(process.env, os.homedir());
  fs.mkdirSync(dir, { recursive: true, mode: 0o700 });
  const file = path.join(dir, crashFileName(now));
  const text = crashText({ cause, kv, when: now, sidecar: sidecarTail.lines(), renderer: rendererTail.lines() });
  fs.writeFileSync(file, text, { mode: 0o600 });
  return file;
}

// Chromium's reasons for a renderer ending all mean it died but 'clean-exit',
// which is a renderer that was asked to go.
export function rendererCrashed(reason: string): boolean {
  return reason !== 'clean-exit';
}

// reportCrash is what a death calls: the file, and where it landed in main's
// console and trace.
export function reportCrash(cause: string, kv: Record<string, string>): void {
  try {
    logLine('error', `[gridwell] crash report written to ${writeCrashFile(cause, kv)}`);
  } catch (err) {
    logLine('error', `[gridwell] crash report not written: ${err instanceof Error ? err.message : String(err)}`);
  }
}
