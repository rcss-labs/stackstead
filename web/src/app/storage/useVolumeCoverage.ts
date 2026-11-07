import { useEffect, useState } from 'react';
import { createBackupsApi } from '$lib/api/backups';
import type { BackupRun } from '$lib/api/backupTypes';
import { useServer } from '../servers/ServerContext';

export type VolumeCoverage = 'unknown' | 'covered' | 'uncovered';

/** A volume is covered when a loaded run backed it up successfully. */
export function coverageOf(runs: BackupRun[], volume: string): 'covered' | 'uncovered' {
  const backedUp = runs.some((run) => run.items.some((item) => item.kind === 'volume' && item.name === volume && item.status === 'ok'));
  return backedUp ? 'covered' : 'uncovered';
}

/** `unknown` until the recent runs load, and whenever they fail, so no warning is shown on a guess. */
export function useVolumeCoverage(volume: string): VolumeCoverage {
  const { local, id } = useServer();
  const base = local ? '' : `/api/servers/${encodeURIComponent(id)}`;
  const [state, setState] = useState<{ volume: string; coverage: VolumeCoverage }>({ volume, coverage: 'unknown' });

  useEffect(() => {
    let current = true;
    createBackupsApi(base)
      .runs()
      .then(
        (runs) => {
          if (current) setState({ volume, coverage: coverageOf(runs, volume) });
        },
        () => {
          if (current) setState({ volume, coverage: 'unknown' });
        },
      );
    return () => {
      current = false;
    };
  }, [base, volume]);

  // A result for a previous volume is not coverage for this one.
  return state.volume === volume ? state.coverage : 'unknown';
}
