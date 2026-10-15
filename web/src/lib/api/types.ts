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
  /** Compose version, or 'unavailable'. */
  compose: string;
  events: 'connected' | 'disconnected';
  metricsDegraded: boolean;
  /** Server build version; empty when unknown. */
  version?: string;
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
