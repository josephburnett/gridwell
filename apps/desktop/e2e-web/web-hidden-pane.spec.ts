import { test } from './fixtures';
import { hiddenPaneScenario } from '../e2e/hidden-pane';

// The browser's half of e2e/hidden-pane.spec.ts: the 2026-10-04 trace was a
// web client's.
test('a pane hidden under a zoomed sibling neither adopts nor writes a framing, across a reload', async ({
  gw,
  serve,
  window,
}) => {
  await hiddenPaneScenario(gw, window, serve.token);
});
