# Client pure-extraction backlog (full sweep, 2026-06-20)

Source: function-by-function audit of ALL 20 `//go:build js && wasm` files
(~9355 lines) by 5 parallel agents. This REPLACES the earlier curated triage
(which mislabeled file_overlay.go as glue and missed the items below). Filter
applied: load-bearing decision + real hazard (trivial pure expressions — color
tables, font shorthand, glyph sizing, nonzero, mouseXY, flag-OR predicates —
are intentionally excluded).

User approved doing ALL of A+B+C. Each item = its own commit, `make check`
green every commit, `make check-electron` after any wasm render/gesture/live
change.

## Tier A — divergence class (preview re-derives what commit decides)
- [ ] A1. Resize close-warn. `drawResizePreview` (gesture_draw.go ~547-600) re-derives
  the side-collapse decision inline, duplicating `gesture.ResizeOutcome` used at
  commit (`commitResize`, right_button.go ~662). Route the preview through the
  SAME pure decision. Home: client/gesture.
- [ ] A2. Divider hover-cursor vs arm. `dividerResizeCursor` (right_button.go ~747-762)
  "mirrors armLeftResize exactly" (~718-738) — two copies of the same gating.
  Extract one pure `ResizeAffordance(inPlus, region, hasDivider) (arm bool, cursor string)`.
- [ ] A3. Drop verdict -> ghost plan. `previewDrop` (drop_target.go 49-117): DecideDrop
  is tested, the verdict->ghost mapping (delete 0.2 shrink+fragment, reject
  sub-cases, forbidden flag, cursor) is not. Extract `GhostPlanForDrop(...)` in
  client/dragdrop + table test.

## Tier B — preview = descent = ascent invariant math
- [ ] B4. Markdown preview scale/scroll. `drawMarkdownNode` (markdown_render.go 62-99):
  cover-crop scale + scroll selection (focused / stored TextW-H / fallback). Home:
  client/markdown. (Overlaps UX #7.)
- [ ] B5. Raw-text baseline + per-line cull. `drawMarkdownText` (markdown_render.go
  474-501): slot/baseline/slotTop math + line-visible cull. Pixel-match contract.
- [ ] B6. URL cover-fit + pan clamp. `drawImageCover` (url_preview.go 41-63) source-rect
  + `clampURLPan` (69-103, already pure). Home: client/coverfit (or client/preview).
- [ ] B7. URL pane-state selection + boot viewport precedence. `encodeFocusedPaneURL`
  (urlsync.go 91-105) TextState-vs-GridState rule; boot viewport precedence in
  `applyURLOnBoot` (146-157, 200-206). Home: client/url.

## Tier C — real correctness hazards
- [ ] C8. `tileAtCell`/`nodeAtCellInGrid` (input.go 89-102, 813-825) reimplement the
  already-tested `dragdrop.TileContainsCell` by hand — call the existing fn.
- [ ] C9. Negative-quadrant cell flooring. `childTileAtScreen` (drop_target.go 254-262)
  hand-rolls floor-toward-neg-inf; reuse `dragdrop.FloorCellAt` or extract `FloorCell`.
- [ ] C10a. onWheel zoom/pan-to-cursor (input.go 160-187). Home: client/zoomtrans.
- [ ] C10b. dividerOnSide adjacency match (right_button.go 830-858). Home: client/pane.
- [ ] C10c. data-URL JPEG parse (snapshotShellCanvas, shell_stream_client.go 528-540).
- [ ] C10d. WebSocket readyState send/queue classifier (sendBinary/sendText 364-384).
- [ ] C10e. startAscent parent-grid resolution loop (input.go 1015-1043).
- [ ] C10f. gridIDForPath traversal (main.go 734-752).

After this backlog: resume UX list #7 then #3.
