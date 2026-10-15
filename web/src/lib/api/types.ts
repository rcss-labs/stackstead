// DTOs mirrored from the Go API (internal/api/*_dto.go, observe.go,
// handlers_auth.go). This file is the shared contract: change it only together
// with the backend. Timestamps are RFC 3339 strings; optional fields are those
// the server omits when empty (`omitempty`).

export type StackStatus = 'running' | 'partial' | 'stopped' | 'unavailable' | 'unknown';
export type Origin = 'managed' | 'external';

export type ContainerHealth = 'healthy' | 'unhealthy' | 'starting' | 'none';

/** Containers of a stack by healthcheck result; `failed` is dead, restarting and non-zero exits. */
export interface HealthCounts {
  healthy: number;
  unhealthy: number;
  starting: number;
  none: number;
  failed: number;
}

export interface StackSummary {
  name: string;
  path: string;
  project: string;
  origin: Origin;
  status: StackStatus;
  serviceCount: number;
  running: number;
  health: HealthCounts;
  /** Published host ports from the compose file; ranges as "8000-8010". */
  ports: string[];
}

export type Managed = 'stack' | 'compose' | 'none';

/** One row of GET /api/containers. */
export interface ContainerRow {
  /** First 12 hex of the container id. */
  id: string;
  name: string;
  image: string;
  state: string;
  status: string;
  health: ContainerHealth;
  /** Null unless the container exited with a parsed code. */
  exitCode: number | null;
  /** RFC 3339; "0001-01-01T00:00:00Z" when unknown. */
  createdAt: string;
  project: string;
  service: string;
  stack: string;
  managed: Managed;
  /** Published ports only. */
  ports: string[];
  networks: Record<string, string>;
  /** Null without a sampler reading. */
  cpu: number | null;
  mem: number | null;
  memLimit: number | null;
  restarts24h: number;
}

export interface ContainerMount {
  type: string;
  source: string;
  destination: string;
  readOnly: boolean;
}

/** GET /api/containers/{id}. Deliberately without env, command, entrypoint, user and host config. */
export interface ContainerDetail {
  /** Full container id. */
  id: string;
  name: string;
  image: string;
  state: string;
  status: string;
  health: ContainerHealth;
  startedAt: string | null;
  finishedAt: string | null;
  exitCode: number | null;
  restartPolicy: string;
  restarts24h: number;
  project: string;
  service: string;
  stack: string;
  managed: Managed;
  ports: string[];
  networks: Record<string, string>;
  mounts: ContainerMount[];
  labels: Record<string, string>;
}

export interface ContainerInfo {
  id: string;
  name: string;
  service: string;
  state: string;
  status: string;
  health: ContainerHealth;
}

/** Fields of a service the form editor can touch; also the `readOnly` vocabulary. */
export type Field =
  | 'image'
  | 'build'
  | 'ports'
  | 'volumes'
  | 'environment'
  | 'networks'
  | 'network_mode'
  | 'limits'
  | 'restart'
  | 'healthcheck';

export interface ServiceConfig {
  image?: string;
  build?: { context: string; dockerfile?: string };
  ports?: string[];
  volumes?: string[];
  environment?: { name: string; value: string }[];
  networks?: { name: string; ipv4Address?: string }[];
  networkMode?: string;
  /** Always present in responses (may be `{}`). */
  limits: { cpus?: string; memory?: string };
  restart?: string;
  healthcheck?: {
    test?: string[];
    interval?: string;
    timeout?: string;
    startPeriod?: string;
    retries?: number;
  };
}

export interface ServiceDetail {
  name: string;
  config: ServiceConfig;
  readOnly: Field[];
  containers: ContainerInfo[];
}

export interface StackDetail extends StackSummary {
  hash: string;
  services: ServiceDetail[];
  parseError?: string;
}

export type OpState = 'running' | 'succeeded' | 'failed' | 'cancelled' | 'timed_out';

export interface Operation {
  id: string;
  stack: string;
  action: string;
  state: OpState;
  startedAt: string;
  endedAt?: string;
  exitCode?: number;
  error?: string;
}

export interface OperationDetail extends Operation {
  lines: string[];
}

export interface FSEntry {
  name: string;
  isDir: boolean;
  size: number;
}

export interface Network {
  name: string;
  driver: string;
  subnets: string[];
}

export interface Status {
  docker: 'ok' | 'unavailable';
  /** Engine version; empty when unknown. */
  dockerVersion: string;
  /** Compose version, or 'unavailable'. */
  compose: string;
  events: 'connected' | 'disconnected';
  metricsDegraded: boolean;
  /** Server build version; empty when unknown. */
  version?: string;
  /** Metrics sampling settings, in seconds; 0 when unset. */
  metricsIntervalSeconds: number;
  metricsRetentionSeconds: number;
}

export interface FieldError {
  field: string;
  message: string;
}

/** `{error}` body of every failed JSON response. */
export interface ErrorBody {
  error: string;
}

/** 400 body for validation failures: per-field messages next to the summary. */
export interface FieldErrorBody extends ErrorBody {
  fields: FieldError[];
}

export interface RawCompose {
  content: string;
  hash: string;
}

/** `hash` of the file after a successful write. */
export interface HashResponse {
  hash: string;
}

export interface LogEvent {
  service: string;
  container: string;
  stream: 'stdout' | 'stderr';
  time: string;
  text: string;
}

export interface LogError {
  service: string;
  container: string;
  error: string;
}

export interface MetricPoint {
  time: string;
  service: string;
  cpu: number;
  mem: number;
  memLimit: number;
  netRxBytesPerSec?: number;
  netTxBytesPerSec?: number;
  blkReadBytesPerSec?: number;
  blkWriteBytesPerSec?: number;
}

/** GET /api/stacks/{name}/metrics response. */
export interface MetricsHistory {
  points: MetricPoint[];
}

/** Raw per-container sample from the live metrics stream (counters are cumulative). */
export interface Sample {
  time: string;
  service: string;
  container: string;
  cpu: number;
  mem: number;
  memLimit: number;
  netRx: number;
  netTx: number;
  blkRead: number;
  blkWrite: number;
}

/** `change` event on /api/events. */
export interface Change {
  project: string;
  seq: number;
}

/** `status` event on /api/events. */
export interface StatusFlags {
  events: 'connected' | 'disconnected';
  metricsDegraded: boolean;
}

export interface SetupStatus {
  needsSetup: boolean;
}

export interface Me {
  username: string;
}

export interface MetricsTokenStatus {
  configured: boolean;
}

export interface MetricsToken {
  token: string;
}

// Request bodies.

export interface LoginRequest {
  username: string;
  password: string;
}

export interface SetupRequest extends LoginRequest {
  setupToken?: string;
}

export interface CreateStackRequest {
  name: string;
  service: string;
  config: ServiceConfig;
}

export interface ImportRequest {
  path: string;
}

export interface UpdateServiceRequest {
  config: ServiceConfig;
  hash: string;
}

/** POST /api/stacks/{name}/services/{service}/preview (same body as the save). */
export type PreviewRequest = UpdateServiceRequest;

/** Candidate file bytes plus the hash the preview was computed against. */
export interface PreviewResponse {
  content: string;
  hash: string;
}

/** PUT raw compose body. */
export type RawComposeBody = RawCompose;

/** POST /api/password. Answers 204; 403 on a wrong `current`. */
export interface ChangePasswordRequest {
  current: string;
  new: string;
}

export const ACTIONS = ['up', 'down', 'restart', 'pull', 'build'] as const;
export type Action = (typeof ACTIONS)[number];

/** GET /api/host. A null section is unavailable on this host; rates are null until two readings exist. */
export interface HostSnapshot {
  hostname: string;
  os: string;
  source: 'proc' | 'docker';
  sampledAt: string;
  containers: { total: number; running: number; exited: number; bad: number } | null;
  cpu: { percent: number | null; cores: number } | null;
  memory: { used: number; total: number; swapUsed: number; swapTotal: number } | null;
  disk: { path: string; free: number; total: number } | null;
  network: { rxBytesPerSec: number | null; txBytesPerSec: number | null; interface: string } | null;
  diskIO: { readBytesPerSec: number | null; writeBytesPerSec: number | null } | null;
  volumes: { count: number; size: number; dangling: number } | null;
  uptime: { seconds: number; load1: number; load5: number; load15: number } | null;
  restarts24h: { total: number; top: HostRestartTop[] } | null;
}

export interface HostRestartTop {
  container: string;
  service: string;
  project: string;
  count: number;
}

/** GET /api/alerts. */
export interface Alert {
  kind: string;
  severity: 'danger' | 'warning' | 'info';
  stack?: string;
  service?: string;
  container?: string;
  message: string;
  since: string;
}

export type SummaryWindow = '5m' | '15m' | '1h';

export interface TopEntry {
  container: string;
  service: string;
  project: string;
  value: number;
}

export interface ProjectSummary {
  cpu: (number | null)[];
  mem: (number | null)[];
  cpuNow: number | null;
  memNow: number | null;
  netRxBytesPerSec: number | null;
  netTxBytesPerSec: number | null;
}

/** GET /api/metrics/summary. `step` is integer seconds; null entries are empty buckets. */
export interface MetricsSummary {
  window: SummaryWindow;
  step: number;
  from: string;
  to: string;
  stacks: Record<string, ProjectSummary>;
  top: { cpu: TopEntry[]; mem: TopEntry[] };
}

/** `event` SSE message. `time` is RFC 3339 UTC, possibly with nanoseconds; parse with Date. */
export interface DockerEvent {
  action: string;
  container: string;
  project: string;
  service: string;
  image: string;
  time: string;
  exitCode?: number;
  health?: 'healthy' | 'unhealthy';
}
