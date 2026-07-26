// Package shellconn holds the pure decision logic for the wasm shell
// attachment (refresh-button visibility, freeze-capture decoding), factored
// out of the wasm client so it is testable without a browser. The WS-
// lifecycle half (send queueing, close-code liveness) died with the WS
// transport (2026-07-26): stream lifecycle now lives in the Electron main
// process (apps/desktop/src/main/shellstreams.ts), tested there.
package shellconn

import "encoding/base64"

// DecodeJPEGDataURL decodes a "data:image/jpeg;base64,..." data URL into the
// raw JPEG bytes. ok is false when s lacks the exact prefix or the base64
// body doesn't decode.
//
// The decode is done in Go on purpose, NOT via JS atob: atob returns a binary
// string whose code units are byte values, but reading it back through
// js.Value.String() re-encodes as UTF-8 and doubles every byte ≥ 0x80,
// corrupting the JPEG (FF D8 … becomes C3 BF C3 98 …).
func DecodeJPEGDataURL(s string) ([]byte, bool) {
	const prefix = "data:image/jpeg;base64,"
	if len(s) <= len(prefix) || s[:len(prefix)] != prefix {
		return nil, false
	}
	out, err := base64.StdEncoding.DecodeString(s[len(prefix):])
	if err != nil {
		return nil, false
	}
	return out, true
}

// RefreshVisibility is the verdict for a frozen shell descent's refresh
// button: whether it shows, and whether the caller must kick off a
// liveness probe (because the tmux session's state is unknown).
type RefreshVisibility struct {
	Show  bool
	Probe bool
}

// DecideShellRefreshVisible decides whether the lower-right refresh
// button paints on a frozen shell descent, and whether a
// ShellSessionAlive probe must be started. Pure: the caller resolves the
// inputs (and performs the probe side effect when Probe is set).
//
//   - not a shell tile        → hidden, no probe.
//   - no preview blob yet      → fresh tile; refresh creates a new tmux
//     session, so always show.
//   - liveness cached alive    → refresh attaches; show.
//   - liveness cached dead      → no recovery possible; hide.
//   - liveness unknown          → hide and probe; a redraw follows when
//     the probe result lands.
//
// aliveKnown is whether the liveness probe result is cached at all;
// alive is that cached value (meaningful only when aliveKnown).
func DecideShellRefreshVisible(isShell, hasPreview, aliveKnown, alive bool) RefreshVisibility {
	if !isShell {
		return RefreshVisibility{}
	}
	if !hasPreview {
		return RefreshVisibility{Show: true}
	}
	if aliveKnown {
		return RefreshVisibility{Show: alive}
	}
	return RefreshVisibility{Probe: true}
}
