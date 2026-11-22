import { test } from './fixtures';
import { closeShell, fastLinkDrawsNothing, openShell, slowLink } from './localecho';

// Local echo across the seam: real keys into xterm, the /shell WebSocket, a
// tmux PTY and its echo, with the client's output held to make the link slow
// (localecho.ts). The engine's unit tests cannot see the overlay, the
// terminal's buffer or a capture.

test('a fast link draws no local echo', async ({ gw, window }) => {
  const at = await openShell(gw);
  await fastLinkDrawsNothing(gw, window);
  await closeShell(gw, at);
});

test('a slow link draws a guess before its echo, never in a capture, never with echo off', async ({ gw, window }) => {
  test.setTimeout(120_000);
  const at = await openShell(gw);
  await slowLink(window, gw);
  await closeShell(gw, at);
});
