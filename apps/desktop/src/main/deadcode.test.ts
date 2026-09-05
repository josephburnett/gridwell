import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readdirSync, statSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join, relative, resolve } from 'node:path';
import ts from 'typescript';

// Unused-export check for the Electron main process and its preloads.
//
// tsc's noUnusedLocals catches a dead local but says nothing about a dead
// export: a function, constant, or type that nothing imports compiles clean
// forever. This walks the real program with the vendored TypeScript compiler,
// so it adds no dependency. For every export of src/main/*.ts and
// src/preload/*.ts it counts references from other non-test files under src/:
// main, preload, and the harnesses (src/harness ships as the check-electron
// gate, so an export only it uses is live). Zero references fails, naming the
// symbol. *.test.ts files do not count; an export whose only reader is its own
// unit test goes in ALLOWED below with a reason, so the exception is written
// down rather than silent.

const here = dirname(fileURLToPath(import.meta.url)); // apps/desktop/src/main
const desktop = resolve(here, '../..');
const srcDir = join(desktop, 'src');

// ALLOWED: "<file relative to src>:<export>" → why the export is kept with no
// non-test reader. Keep it short; every entry is a standing exception.
const ALLOWED: Record<string, string> = {
  'main/viewutil.ts:parseHistory':
    'the pure validator behind reviveNavigation; its clamping and rejection cases are unit-tested directly',
  'main/viewutil.ts:classifyRightPress':
    'the reference implementation of the right-press classification the sandboxed preload must inline; unit-tested as the owner',
  'main/focusguard.ts:GuardInput':
    'the decision\'s input shape; the executor builds it inline as an object literal, so only the table test names the type',
  'main/focusguard.ts:GuardAction':
    'the decision\'s output shape; the executor switches on act.kind, so only the table test names the type',
};

function walkTs(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) walkTs(p, out);
    else if (name.endsWith('.ts') && !name.endsWith('.test.ts') && !name.endsWith('.d.ts')) out.push(p);
  }
  return out;
}

function isChecked(file: string): boolean {
  const rel = relative(srcDir, file);
  return rel.startsWith('main/') || rel.startsWith('preload/');
}

function resolveAlias(checker: ts.TypeChecker, sym: ts.Symbol): ts.Symbol {
  return sym.flags & ts.SymbolFlags.Alias ? checker.getAliasedSymbol(sym) : sym;
}

test('every export of src/main and src/preload has a reader outside its own file', () => {
  const files = walkTs(srcDir);
  const cfg = ts.readConfigFile(join(desktop, 'tsconfig.json'), ts.sys.readFile);
  assert.ok(!cfg.error, 'tsconfig.json must parse');
  const parsed = ts.parseJsonConfigFileContent(cfg.config, ts.sys, desktop);
  const program = ts.createProgram(files, parsed.options);
  const checker = program.getTypeChecker();

  // One pass over every non-test file: which files reference which symbol.
  const readers = new Map<ts.Symbol, Set<string>>();
  for (const file of files) {
    const sf = program.getSourceFile(file);
    assert.ok(sf, `program is missing ${file}`);
    const visit = (node: ts.Node) => {
      if (ts.isIdentifier(node)) {
        const sym = checker.getSymbolAtLocation(node);
        if (sym) {
          const target = resolveAlias(checker, sym);
          let set = readers.get(target);
          if (!set) readers.set(target, (set = new Set()));
          set.add(file);
        }
      }
      ts.forEachChild(node, visit);
    };
    visit(sf);
  }

  const dead: string[] = [];
  const staleAllow = new Set(Object.keys(ALLOWED));
  for (const file of files.filter(isChecked)) {
    const sf = program.getSourceFile(file)!;
    const moduleSym = checker.getSymbolAtLocation(sf);
    if (!moduleSym) continue; // a script with no exports
    for (const exp of checker.getExportsOfModule(moduleSym)) {
      const key = `${relative(srcDir, file)}:${exp.name}`;
      const target = resolveAlias(checker, exp);
      const from = readers.get(target) ?? new Set<string>();
      const used = [...from].some((f) => f !== file);
      if (key in ALLOWED) {
        staleAllow.delete(key);
        if (used) dead.push(`${key} is in ALLOWED but has a reader now — drop the entry`);
        continue;
      }
      if (!used) dead.push(`${key} has no reader outside its file — delete it, drop the export, or add it to ALLOWED with a reason`);
    }
  }
  for (const key of staleAllow) dead.push(`ALLOWED names ${key}, which is not an export any more — drop the entry`);
  assert.equal(dead.length, 0, 'dead exports:\n  ' + dead.join('\n  '));
});
