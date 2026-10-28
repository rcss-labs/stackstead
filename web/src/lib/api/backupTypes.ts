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

export interface BackupEncryption {
  /** Archives are encrypted and authenticated with the stored passphrase. */
  enabled: boolean;
  /** Write-only: sent on PUT when typed, never returned. */
  passphrase?: string;
  passphraseSet: boolean;
  /** Short id of the stored passphrase; also written into every archive sidecar. */
  keyId: string;
}

export interface BackupConfig {
  schedule: BackupSchedule;
  local: BackupLocal;
  s3: BackupS3;
  encryption: BackupEncryption;
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
  /** An encrypted archive (.tar.gz.enc); read from the name. */
  encrypted: boolean;
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
  /** Write-only: opens an encrypted archive; empty uses the passphrase stored on the server. */
  passphrase?: string;
  /** Confirms restoring an archive that is neither encrypted nor authenticated. */
  allowUnencrypted?: boolean;
  /** Starts the stack once it is restored; omitted means the stack is left stopped. */
  start?: boolean;
}

/** A host-reaching setting in an archived compose file. */
export interface BackupComposeRisk {
  stack: string;
  file: string;
  service: string;
  /** privileged, cap_add, device, host_bind, host_namespace, security_opt or unparseable. */
  kind: string;
  detail: string;
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
  /** False for an archive with no authentication at all. */
  encrypted: boolean;
  keyId: string;
  /** The archive was written by another instance: the restore defaults to leaving the stack stopped. */
  foreign: boolean;
  risks: BackupComposeRisk[];
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
  /** From the sidecar, display only. */
  encrypted: boolean;
  keyId: string;
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

export interface HostRestoreSummary {
  manifest: BackupManifest;
  conflicts: HostRestoreConflict[];
  conflictsDigest: string;
  /** False for an archive that is neither encrypted nor authenticated. */
  encrypted: boolean;
  keyId: string;
  /** Not written by this instance, which includes every restore onto a fresh host. */
  foreign: boolean;
  risks: BackupComposeRisk[];
}

export interface HostRestoreStarted {
  restoreToken: string;
  operation: Operation;
}
