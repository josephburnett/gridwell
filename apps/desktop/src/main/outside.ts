// The renderer's open-outside verb: a route sent an address to the default
// browser. This is main's own call to shell.openExternal, apart from the
// permission a page is denied (viewutil.allowPermission).
import type { TraceEvent } from './trace';
import { viewOpenedOutside } from './viewtrace';
import { webUrl } from './viewutil';

// openOutside hands an http(s) address to open and refuses any other, so a
// route can never launch a mailto: or file: handler. Every verdict is traced.
export async function openOutside(
  url: string,
  open: (url: string) => Promise<void>,
  record: (ev: TraceEvent) => void,
): Promise<void> {
  if (webUrl(url) === null) {
    record(viewOpenedOutside(url, 'refused'));
    throw new Error('only an http(s) address opens outside');
  }
  try {
    await open(url);
  } catch (err) {
    record(viewOpenedOutside(url, 'failed'));
    throw err;
  }
  record(viewOpenedOutside(url, 'opened'));
}
