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

export interface BackupArchiveRef {
  dest: 'local' | 's3';
  name: string;
}

/** Body of POST /api/backups/restore and /api/backups/restore/plan. */
export interface BackupRestoreRequest {
  archive: BackupArchiveRef;
  stack: string;
  /** Restores under another name (also the compose project); empty keeps the name. */
  newName: string;
  files: boolean;
  volumes: boolean;
  images: boolean;
  /** Limits the volumes restored; empty means all of them. */
  volumeKeys: string[];
}

export interface BackupFileDiff {
  path: string;
  old: string;
  new: string;
}

export interface BackupPlannedVolume {
  key: string;
  kind: string;
  name: string;
  target: string;
  bytes: number;
  skip: boolean;
  reason: string;
}

export interface BackupRestoreNetwork {
  name: string;
  driver: string;
}

export interface BackupRestorePlan {
  stack: string;
  targetName: string;
  targetDir: string;
  composePath: string;
  project: string;
  origin: string;
  /** A stack named targetName exists here and is replaced. */
  overwrites: boolean;
  fileDiffs: BackupFileDiff[];
  volumes: BackupPlannedVolume[];
  images: string[];
  missingNetworks: BackupRestoreNetwork[];
  crossHost: boolean;
  warnings: string[];
}

/** The 202 body of POST /api/backups/restore. */
export interface BackupRestoreStarted {
  operation: Operation;
}

export interface BackupManifestVolume {
  key: string;
  kind: string;
  name: string;
  target: string;
  bytes: number;
}

export interface BackupManifestStack {
  name: string;
  composePath: string;
  origin: string;
  project: string;
  wasRunning: boolean;
  networks: string[];
  volumes: BackupManifestVolume[];
  images: string[];
  liveCopy: boolean;
}

export interface BackupManifest {
  format: number;
  stacksteadVersion: string;
  host: string;
  hostSlug: string;
  instanceId: string;
  createdAt: string;
  status: string;
  stacks: BackupManifestStack[];
  networks: number;
  items: BackupItem[];
}
