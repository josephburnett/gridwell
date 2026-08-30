import { sweepLeakedHomes } from './homes';

// Runs once before the suite: clean up whatever a previous aborted run left
// behind. See sweepLeakedHomes in homes.ts.
export default function globalSetup(): void {
  sweepLeakedHomes();
}
