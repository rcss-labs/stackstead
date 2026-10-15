import type {
  Action,
  ChangePasswordRequest,
  CreateStackRequest,
  FSEntry,
  FieldError,
  Alert,
  HashResponse,
  HostSnapshot,
  ImportRequest,
  LoginRequest,
  Me,
  MetricsHistory,
  MetricsSummary,
  MetricsToken,
  MetricsTokenStatus,
  Network,
  Operation,
  OperationDetail,
  PreviewResponse,
  RawCompose,
  ServiceConfig,
  SetupRequest,
  SetupStatus,
  StackDetail,
  StackSummary,
  Status,
  SummaryWindow,
  UpdateServiceRequest,
} from './types';

export class ApiError extends Error {
  status: number;
  fields: FieldError[];
  body?: unknown;

  constructor(status: number, message: string, fields: FieldError[] = [], body?: unknown) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.fields = fields;
    this.body = body;
  }
}

export interface RequestResult<T> {
  data: T;
  checksPartial: boolean;
}

type Method = 'GET' | 'POST' | 'PUT' | 'DELETE';

let unauthorizedHandler: (() => void) | undefined;
let unauthorizedFired = false;

/** App registers the redirect to /login. Registering also re-arms the once-per-session guard. */
export function onUnauthorized(handler: () => void): void {
  unauthorizedHandler = handler;
  unauthorizedFired = false;
}

/** Called after a successful login so a later session expiry redirects again. */
export function rearmUnauthorized(): void {
  unauthorizedFired = false;
}

// These answer 401 for reasons other than "your session is gone".
const AUTH_PATHS = ['/api/login', '/api/logout', '/api/setup', '/api/me'];

function isAuthPath(path: string): boolean {
  const p = path.split('?')[0];
  return AUTH_PATHS.some((a) => p === a || p.startsWith(a + '/'));
}

function notifyUnauthorized(path: string): void {
  if (isAuthPath(path) || unauthorizedFired) return;
  unauthorizedFired = true;
  unauthorizedHandler?.();
}

export async function request<T>(
  method: Method,
  path: string,
  body?: unknown,
  opts: { signal?: AbortSignal } = {},
): Promise<RequestResult<T>> {
  const headers: Record<string, string> = { Accept: 'application/json' };
  const init: RequestInit = {
    method,
    headers,
    credentials: 'same-origin',
    // Never follow a redirect into another origin; a redirect is a failure.
    redirect: 'error',
    signal: opts.signal,
  };
  if (method !== 'DELETE' && method !== 'GET') {
    headers['Content-Type'] = 'application/json';
    init.body = JSON.stringify(body ?? {});
  } else if (method === 'GET' && body !== undefined) {
    throw new TypeError('GET requests cannot have a body');
  }

  let res: Response;
  try {
    res = await fetch(path, init);
  } catch (err) {
    if (opts.signal?.aborted) throw opts.signal.reason ?? err;
    if (err instanceof DOMException && err.name === 'AbortError') throw err;
    throw new ApiError(0, 'network error');
  }

  const checksPartial = res.headers.get('X-Stackstead-Checks') === 'partial';
  if (res.status === 204) return { data: undefined as T, checksPartial };

  let parsed: unknown;
  try {
    parsed = await res.json();
  } catch (err) {
    if (res.ok) {
      // A successful status with an unreadable body is not a success.
      if (opts.signal?.aborted) throw opts.signal.reason ?? err;
      throw new ApiError(res.status, 'invalid response');
    }
    parsed = undefined;
  }

  if (!res.ok) {
    const b = (parsed && typeof parsed === 'object' ? parsed : {}) as { error?: unknown; fields?: unknown };
    const message = typeof b.error === 'string' && b.error ? b.error : `HTTP ${res.status}`;
    const fields = Array.isArray(b.fields) ? (b.fields as FieldError[]) : [];
    if (res.status === 401) notifyUnauthorized(path);
    throw new ApiError(res.status, message, fields, parsed);
  }
  return { data: parsed as T, checksPartial };
}

const seg = encodeURIComponent;

function query(params: Record<string, string | undefined>): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== '') q.set(k, v);
  const s = q.toString();
  return s ? `?${s}` : '';
}

const get = <T>(path: string, signal?: AbortSignal) => request<T>('GET', path, undefined, { signal }).then((r) => r.data);
const post = <T>(path: string, body: unknown = {}) => request<T>('POST', path, body).then((r) => r.data);

export const api = {
  setupStatus: () => get<SetupStatus>('/api/setup/status'),
  setup: (username: string, password: string, setupToken?: string) => {
    const body: SetupRequest = { username, password };
    if (setupToken) body.setupToken = setupToken;
    return post<void>('/api/setup', body);
  },
  login: (username: string, password: string) => post<void>('/api/login', { username, password } satisfies LoginRequest),
  logout: () => post<void>('/api/logout'),
  me: () => get<Me>('/api/me'),

  stacks: () => get<StackSummary[]>('/api/stacks'),
  stack: (name: string) => get<StackDetail>(`/api/stacks/${seg(name)}`),
  /** Resolves with the result plus whether port/IP checks ran without the engine. */
  createStack: (req: CreateStackRequest) => request<StackSummary>('POST', '/api/stacks', req),
  importStack: (path: string) => request<StackSummary>('POST', '/api/stacks/import', { path } satisfies ImportRequest),
  compose: (name: string) => get<RawCompose>(`/api/stacks/${seg(name)}/compose`),
  saveCompose: (name: string, content: string, hash: string) =>
    request<HashResponse>('PUT', `/api/stacks/${seg(name)}/compose`, { content, hash } satisfies RawCompose),
  updateService: (name: string, service: string, config: ServiceConfig, hash: string) =>
    request<HashResponse>('PUT', `/api/stacks/${seg(name)}/services/${seg(service)}`, {
      config,
      hash,
    } satisfies UpdateServiceRequest),
  previewService: (name: string, service: string, config: ServiceConfig, hash: string) =>
    post<PreviewResponse>(`/api/stacks/${seg(name)}/services/${seg(service)}/preview`, {
      config,
      hash,
    } satisfies UpdateServiceRequest),
  act: (name: string, action: Action) => post<Operation>(`/api/stacks/${seg(name)}/actions/${seg(action)}`),
  remove: (name: string, withVolumes: boolean) =>
    request<Operation>('DELETE', `/api/stacks/${seg(name)}${withVolumes ? '?volumes=true' : ''}`).then((r) => r.data),
  stackOperations: (name: string) => get<Operation[]>(`/api/stacks/${seg(name)}/operations`),
  operation: (id: string) => get<OperationDetail>(`/api/operations/${seg(id)}`),
  cancel: (id: string) => post<Operation>(`/api/operations/${seg(id)}/cancel`),
  runningOperations: () => get<Operation[]>('/api/operations?state=running'),

  fs: (path?: string) => get<FSEntry[]>(`/api/fs${query({ path })}`),
  networks: () => get<Network[]>('/api/networks'),
  status: () => get<Status>('/api/status'),
  host: () => get<HostSnapshot>('/api/host'),
  alerts: () => get<Alert[]>('/api/alerts'),
  metricsSummary: (window?: SummaryWindow) => get<MetricsSummary>(`/api/metrics/summary${query({ window })}`),

  metricsTokenStatus: () => get<MetricsTokenStatus>('/api/settings/metrics-token'),
  createMetricsToken: () => post<MetricsToken>('/api/settings/metrics-token'),
  deleteMetricsToken: () => request<void>('DELETE', '/api/settings/metrics-token').then((r) => r.data),
  changePassword: (current: string, next: string) =>
    post<void>('/api/password', { current, new: next } satisfies ChangePasswordRequest),
  metricsHistory: (
    name: string,
    p: { from?: string; to?: string; step?: string; service?: string },
    signal?: AbortSignal,
  ) =>
    request<MetricsHistory>(
      'GET',
      `/api/stacks/${seg(name)}/metrics${query({ from: p.from, to: p.to, step: p.step, service: p.service })}`,
      undefined,
      { signal },
    ).then((r) => r.data),
};
