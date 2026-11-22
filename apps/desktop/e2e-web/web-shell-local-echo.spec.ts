import { test } from './fixtures';
import { closeShell, fastLinkDrawsNothing, openShell, slowLink } from '../e2e/localecho';

// Local echo in a plain browser, the phone's client: the same scenarios as
// the Electron suite's shell-local-echo.spec.ts.

test('a browser shell draws local echo only on a slow link, never with echo off', async ({ gw, window }) => {
  test.setTimeout(120_000);
  const at = await openShell(gw);
  await fastLinkDrawsNothing(gw, window);
  await slowLink(window, gw);
  await closeShell(gw, at);
});
