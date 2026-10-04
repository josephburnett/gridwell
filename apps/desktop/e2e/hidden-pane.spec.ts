import { test, homePassword, loginToken } from './fixtures';
import { hiddenPaneScenario } from './hidden-pane';

// See hidden-pane.ts; e2e-web/web-hidden-pane.spec.ts is the browser's half.
test('a pane hidden under a zoomed sibling neither adopts nor writes a framing, across a reload', async ({
  gw,
  home,
  window,
}) => {
  await hiddenPaneScenario(gw, window, await loginToken(gw.origin, homePassword(home)));
});
