# Vendored xterm.js

Minified UMD bundles copied verbatim from the npm packages (`lib/*.js`,
`css/xterm.css`), loaded by `web/index.html` script tags. Upgraded for issue
#175 (Claude Code scatter): DEC 2026 synchronized output exists only in
xterm ≥ 6, and Unicode-11 widths need the unicode11 addon.

| file | package | version |
|---|---|---|
| xterm.min.js / xterm.min.css | @xterm/xterm | 6.0.0 |
| addon-fit.min.js | @xterm/addon-fit | 0.11.0 |
| addon-webgl.min.js | @xterm/addon-webgl | 0.19.0 |
| addon-unicode11.min.js | @xterm/addon-unicode11 | 0.9.0 |

The canvas addon is retired: it has no stable xterm-6 release, and its
dirty-region artifact class (issue #84) is exactly what the WebGL renderer
exists to avoid — the fallback is now xterm's built-in DOM renderer
(`rendererKind == "dom"`, see `client/wasm/shell_stream_client.go`).

xterm 6 gates proposed APIs (`parser.registerOscHandler`, `term.unicode`)
behind `allowProposedApi: true` — set at Terminal construction; removing it
panics the wasm on shell open.

To upgrade: `npm pack @xterm/xterm@<v> @xterm/addon-...`, copy `package/lib/`
bundles over these filenames, update this table, and run the shell e2e specs
(`ephemeral-shell` asserts the WebGL renderer attached).
