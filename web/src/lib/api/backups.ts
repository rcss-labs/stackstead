import { request } from './client';
import type {
  BackupArchive,
  BackupConfig,
  BackupRun,
  BackupRunStarted,
  BackupS3,
  BackupStack,
  BackupStackSettings,
} from './backupTypes';

import type { ServerSelf } from './types';

type Verb = 'GET' | 'POST' | 'PUT' | 'DELETE';

/** Backup routes on a server; `base` is '' for this server or '/api/servers/{id}' for a connected one. */
export function createBackupsApi(base: string) {
  const call = <T>(method: Verb, path: string, body?: unknown) =>
    request<T>(method, `${base}/api/backups${path}`, body).then((r) => r.data);

  return {
    /** This server's identity; the slug is what its archives are tagged with. */
    self: () => request<ServerSelf>('GET', `${base}/api/peers/self`).then((r) => r.data),
    config: () => call<BackupConfig>('GET', '/config'),
    saveConfig: (config: BackupConfig) => call<BackupConfig>('PUT', '/config', config),
    clearS3Secret: () => call<void>('DELETE', '/config/s3-secret'),
    testS3: (s3: BackupS3) => call<void>('POST', '/s3/test', s3),
    stacks: () => call<BackupStack[]>('GET', '/stacks'),
    setStack: (name: string, settings: BackupStackSettings) =>
      call<void>('PUT', `/stacks/${encodeURIComponent(name)}`, settings),
    run: () => call<BackupRunStarted>('POST', '/run', {}),
    runs: (limit?: number) => call<BackupRun[]>('GET', limit === undefined ? '/runs' : `/runs?limit=${limit}`),
    archives: () => call<BackupArchive[]>('GET', '/archives'),
  };
}

export type BackupsApi = ReturnType<typeof createBackupsApi>;
