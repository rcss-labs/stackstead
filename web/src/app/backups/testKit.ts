import type { BackupConfig } from '$lib/api/backupTypes';

export const sampleConfig: BackupConfig = {
  schedule: { enabled: true, frequency: 'daily', weekday: 0, time: '03:00' },
  local: { enabled: true, path: '/var/backups', keepLast: 7 },
  s3: {
    enabled: false,
    bucket: 'b',
    prefix: '',
    region: 'us-east-1',
    endpoint: '',
    pathStyle: false,
    accessKeyId: 'AKIA',
    secretKeySet: true,
  },
  nextRun: '2026-10-27T03:00:00Z',
  pausedAfterRestore: false,
};
