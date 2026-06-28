# TODO

As you fix things mark them as [x] if you are sure its fixed.
If you want verification or are unsure, mark it with [~].

- [~] middle button ascend doesn't work in text and url panes (maybe just live urls)
      (text-edit overlay + live-URL view both now forward the middle press to ascend; needs a run to confirm)
- [x] cannot see pane creation preview line on grey text background
      (dark casing stroked under the split-preview line so it reads on any background)
- [x] preview tiles in markdown rendered views should be proper preview tiles, not just colored boxes, and they should be square
      (embeds now render the real one-level preview via drawNodeWithPreview, centered as a square)
- [~] clicking on a preview tile in a markdown rendered pane is not descending into it, it should
      (broadened descent to all descendable kinds incl. file/process-well + shell; CROSS-GRID embed targets are still unsupported — if your case is one, that needs the bigger path-resolution work)
- [x] url jpg preview quality is too low
      (JPEG quality 70 -> 92)
- [~] target _blank popups urls open in a new window. can i just follow them instead without triggering cloudflare's bot guards?
      (window.open / target=_blank now follows in the same view; needs a run to confirm bot guards stay happy)
- [~] f11 doesn't always work to enter and exit full screen mode
      (live-URL view now mirrors the F11 handler so it works while a URL tile has focus; needs a run to confirm)
- [~] touch screen events aren't going through (dragging to scroll on screen with my finger, or poking)
      (single-finger touch now synthesized into left-button mouse gestures; needs a touch device to confirm; multi-finger left to the browser)
- [~] app doesn't respond to screen rotation when in full screen (like change of aspect dimensions)
      (best-effort: re-fit a fullscreen window to its display on display-metrics-changed; needs a run to confirm)
- [~] i cannot minimize the app, just maximize or f11 full screen
      (set minimizable explicitly + added Ctrl/Cmd+M to minimize; whether the WM shows a minimize button is its call)
- [x] the app starts windowed, but bigger than my screen. i would like it to start maximized or fit within the screen
      (initial bounds = display work area, then maximize)
- [ ] the + menu can't be opened on a split's inner pane: its corner + button overlaps the
      adjacent divider's resize band, so armLeftResize swallows the click before the plus-toggle.
      (found while writing the menu-focus e2e in Phase 1a; gesture-priority/geometry issue, not menu state.
       fix: let a click inside the + circle win over the divider resize arm, and cover it in the e2e.)
