import { sweepLeakedHomes } from './homes';
import { snapshotTree } from './runtree';

// Runs once before the suite; see sweepLeakedHomes in homes.ts and snapshotTree
// in runtree.ts.
export default function globalSetup(): void {
  sweepLeakedHomes();
  console.log(`[e2e] this run tests the artifacts pinned at ${snapshotTree()}`);
}
