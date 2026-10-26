// Backup wire types. Mirrors internal/apitypes/backups.go 1:1; change both together.
import type { Operation } from './types';

export interface BackupSchedule {
  enabled: boolean;
  frequency: 'daily' | 'weekly';
  /** 0 (Sunday) to 6; used for weekly. */
  weekday: number;
  /** HH:MM in the host's time zone. */
  time: string;
}

export interface BackupLocal {
  enabled: boolean;
  path: string;
  keepLast: number;
}

export interface BackupS3 {
  enabled: boolean;
  bucket: string;
  prefix: string;
  region: string;
  endpoint: string;
  pathStyle: boolean;
  accessKeyId: string;
  /** Accepted on PUT and the S3 test, never returned; omitted keeps the stored secret. */
  secretKey?: string;
  secretKeySet: boolean;
}

export interface BackupConfig {
  schedule: BackupSchedule;
  local: BackupLocal;
  s3: BackupS3;
  /** When the schedule fires next; absent when it is off. Ignored on PUT. */
  nextRun?: string;
  /** A host restore turned the schedule off; saving it enabled clears this. Ignored on PUT. */
  pausedAfterRestore: boolean;
}

export interface BackupMount {
  kind: 'volume' | 'bind';
  name: string;
  target: string;
  /** -1 when the size is unknown. */
  bytes: number;
}

export interface BackupStackSettings {
  volumes: boolean;
  images: boolean;
  stopDuring: boolean;
}

export interface BackupStack extends BackupStackSettings {
  stack: string;
  mounts: BackupMount[];
}

export interface BackupItem {
  kind: 'base' | 'volume' | 'image' | 'stack-files' | 'upload';
  stack?: string;
  name?: string;
  status: 'ok' | 'failed' | 'skipped';
  note?: string;
  bytes?: number;
}

export interface BackupRun {
  id: number;
  startedAt: string;
  endedAt?: string;
  trigger: 'manual' | 'schedule';
  status: 'running' | 'success' | 'partial' | 'failed';
  archive?: string;
  size: number;
  destinations: string[];
  items: BackupItem[];
  error?: string;
}

export interface BackupArchive {
  dest: 'local' | 's3';
  name: string;
  hostSlug: string;
  createdAt: string;
  size: number;
}

/** The 202 body of POST /api/backups/run. */
export interface BackupRunStarted {
  operation: Operation;
}

// Whole-host restore from the setup screen (/api/setup/restore/...).

export interface HostRestoreS3 {
  bucket: string;
  /** Full object key. */
  key: string;
  region: string;
  endpoint: string;
  pathStyle: boolean;
  accessKeyId: string;
  /** Sent, never returned. */
  secretKey?: string;
}

export interface HostRestoreSource {
  kind: 's3' | 'path' | 'upload';
  path?: string;
  s3?: HostRestoreS3;
}

export interface HostRestoreConflict {
  kind: 'volume' | 'network' | 'container' | 'stack-dir';
  name: string;
  action: 'overwrite' | 'recreate' | 'keep';
}

export interface BackupManifestVolume {
  key: string;
  kind: 'volume' | 'bind';
  name: string;
  target: string;
  bytes: number;
}

export interface BackupManifestStack {
  name: string;
  wasRunning: boolean;
  volumes: BackupManifestVolume[];
  images: string[];
}

export interface HostRestoreSummary {
  manifest: {
    stacksteadVersion: string;
    host: string;
    createdAt: string;
    status: string;
    stacks: BackupManifestStack[];
    networks: number;
    items: BackupItem[];
  };
  conflicts: HostRestoreConflict[];
  conflictsDigest: string;
}

export interface HostRestoreStarted {
  restoreToken: string;
  operation: Operation;
}
