import type {
  Action,
  ActionEntry, StorageOverview, StorageImages, StorageVolumes, StorageBuildCache,
  StorageImageDetail, StorageVolumeDetail, StoragePlan, StoragePlanRequest,
  AddServiceRequest,
  ChangePasswordRequest,
  DockerfileContent,
  DockerfileView,
  ContainerDetail,
  ContainerRow,
  CreateStackRequest,
  CreateNetworkRequest,
  HostInterface,
  FSEntry,
  FieldError,
  Alert,
  HashResponse,
  HostSnapshot,
  ImportRequest,
  LoginRequest,
  Me,
  HealthHistory,
  MetricsHistory,
  MetricsSummary,
  MetricsToken,
  MetricsTokenStatus,
  UpdateStatus,
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
  AddPeerRequest,
  ConnectionToken,
  FleetStacks,
  PeerProbe,
  PeerProbeRequest,
  PeerView,
  ServerSelf,
  ServerSelfUpdate,
  UpdateServiceRequest,
} from './types';
import { createImagesApi, createLocalImagesApi } from './images';

export class ApiError extends Error {
  status: number;
  fields: FieldError[];
  body?: unknown;
  /** Machine-readable failure reason, e.g. `peer_unreachable`; absent on plain errors. */
  code?: string;

  constructor(status: number, message: string, fields: FieldError[] = [], body?: unknown, code?: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.fields = fields;
    this.body = body;
    this.code = code;
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

export function notifyUnauthorized(path: string): void {
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
    const b = (parsed && typeof parsed === 'object' ? parsed : {}) as { error?: unknown; fields?: unknown; code?: unknown };
    const message = typeof b.error === 'string' && b.error ? b.error : `HTTP ${res.status}`;
    const fields = Array.isArray(b.fields) ? (b.fields as FieldError[]) : [];
    if (res.status === 401) notifyUnauthorized(path);
    throw new ApiError(res.status, message, fields, parsed, typeof b.code === 'string' && b.code ? b.code : undefined);
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

/** Every member takes paths relative to `base` ('' = this server, '/api/servers/{id}' = a connected one). */
function helpers(base: string) {
  const call = <T>(method: Method, path: string, body?: unknown, opts: { signal?: AbortSignal } = {}) =>
    request<T>(method, base + path, body, opts);
  const get = <T>(path: string, signal?: AbortSignal) => call<T>('GET', path, undefined, { signal }).then((r) => r.data);
  const post = <T>(path: string, body: unknown = {}) => call<T>('POST', path, body).then((r) => r.data);
  return { call, get, post };
}

export function createApi(base: string) {
  const { call, get, post } = helpers(base);

  return {
    setupStatus: () => get<SetupStatus>('/api/setup/status'),
    setup: (username: string, password: string, setupToken?: string) => {
      const body: SetupRequest = { username, password };
      if (setupToken) body.setupToken = setupToken;
      return post<void>('/api/setup', body);
    },
    login: (username: string, password: string) => post<void>('/api/login', { username, password } satisfies LoginRequest),
    logout: () => post<void>('/api/logout'),
    me: () => get<Me>('/api/me'),

    containers: () => get<ContainerRow[]>('/api/containers'),
    container: (id: string) => get<ContainerDetail>(`/api/containers/${seg(id)}`),
    containerAction: (id: string, action: 'start' | 'stop' | 'restart') =>
      post<ContainerRow | undefined>(`/api/containers/${seg(id)}/${seg(action)}`), // 204 when the action removed the container

    storageOverview: () => get<StorageOverview>('/api/storage'),
    storageImages: () => get<StorageImages>('/api/storage/images'),
    storageVolumes: () => get<StorageVolumes>('/api/storage/volumes'),
    storageBuildCache: () => get<StorageBuildCache>('/api/storage/build-cache'),
    storageImage: (id: string) => get<StorageImageDetail>(`/api/storage/images/${seg(id)}`),
    storageVolume: (name: string) => get<StorageVolumeDetail>(`/api/storage/volumes/${seg(name)}`),
    createStoragePlan: (body: StoragePlanRequest) => post<StoragePlan>('/api/storage/plans', body),
    storagePlan: (id: string) => get<StoragePlan>(`/api/storage/plans/${seg(id)}`),
    confirmStoragePlan: (id: string, confirm: string) => post<Operation>(`/api/storage/plans/${seg(id)}/confirm`, { confirm }),
    refreshStorage: () => post<{ startedAt: string }>('/api/storage/refresh'),
    recentActions: (params: { kind?: string; scope?: string; limit?: number } = {}) =>
      get<ActionEntry[]>(`/api/actions${query({ kind: params.kind, scope: params.scope, limit: params.limit?.toString() })}`),

    stacks: () => get<StackSummary[]>('/api/stacks'),
    stack: (name: string) => get<StackDetail>(`/api/stacks/${seg(name)}`),
    /** Resolves with the result plus whether port/IP checks ran without the engine. */
    createStack: (req: CreateStackRequest) => call<StackSummary>('POST', '/api/stacks', req),
    importStack: (path: string) => call<StackSummary>('POST', '/api/stacks/import', { path } satisfies ImportRequest),
    compose: (name: string) => get<RawCompose>(`/api/stacks/${seg(name)}/compose`),
    saveCompose: (name: string, content: string, hash: string) =>
      call<HashResponse>('PUT', `/api/stacks/${seg(name)}/compose`, { content, hash } satisfies RawCompose),
    updateService: (name: string, service: string, config: ServiceConfig, hash: string, dockerfile?: DockerfileContent) =>
      call<HashResponse>('PUT', `/api/stacks/${seg(name)}/services/${seg(service)}`, {
        config,
        hash,
        ...(dockerfile ? { dockerfile } : {}),
      } satisfies UpdateServiceRequest),
    previewService: (name: string, service: string, config: ServiceConfig, hash: string) =>
      post<PreviewResponse>(`/api/stacks/${seg(name)}/services/${seg(service)}/preview`, {
        config,
        hash,
      } satisfies UpdateServiceRequest),
    addService: (name: string, service: string, config: ServiceConfig, hash: string, dockerfile?: DockerfileContent) =>
      call<StackDetail>('POST', `/api/stacks/${seg(name)}/services`, {
        service,
        config,
        hash,
        ...(dockerfile ? { dockerfile } : {}),
      } satisfies AddServiceRequest),
    dockerfile: (name: string, service: string) =>
      get<DockerfileView>(`/api/stacks/${seg(name)}/services/${seg(service)}/dockerfile`),
    saveDockerfile: (name: string, service: string, content: string, hash: string) =>
      call<HashResponse>('PUT', `/api/stacks/${seg(name)}/services/${seg(service)}/dockerfile`, {
        content,
        hash,
      } satisfies DockerfileContent).then((r) => r.data),
    upService: (name: string, service: string) =>
      post<Operation>(`/api/stacks/${seg(name)}/services/${seg(service)}/actions/up`),
    previewAddService: (name: string, service: string, config: ServiceConfig, hash: string) =>
      post<PreviewResponse>(`/api/stacks/${seg(name)}/services/preview`, {
        service,
        config,
        hash,
      } satisfies AddServiceRequest),
    removeService: (name: string, service: string, hash: string) =>
      call<StackDetail>('DELETE', `/api/stacks/${seg(name)}/services/${seg(service)}?hash=${seg(hash)}`).then((r) => r.data),
    act: (name: string, action: Action) => post<Operation>(`/api/stacks/${seg(name)}/actions/${seg(action)}`),
    remove: (name: string, withVolumes: boolean) =>
      call<Operation>('DELETE', `/api/stacks/${seg(name)}${withVolumes ? '?volumes=true' : ''}`).then((r) => r.data),
    stackOperations: (name: string) => get<Operation[]>(`/api/stacks/${seg(name)}/operations`),
    operation: (id: string) => get<OperationDetail>(`/api/operations/${seg(id)}`),
    cancel: (id: string) => post<Operation>(`/api/operations/${seg(id)}/cancel`),
    runningOperations: () => get<Operation[]>('/api/operations?state=running'),

    fs: (path?: string) => get<FSEntry[]>(`/api/fs${query({ path })}`),
    networks: () => get<Network[]>('/api/networks'),
    createNetwork: (body: CreateNetworkRequest) => post<Network>('/api/networks', body),
    deleteNetwork: (id: string) => call<void>('DELETE', `/api/networks/${seg(id)}`).then((r) => r.data),
    hostInterfaces: () => get<HostInterface[]>('/api/host/interfaces'),
    status: () => get<Status>('/api/status'),
    host: () => get<HostSnapshot>('/api/host'),
    alerts: () => get<Alert[]>('/api/alerts'),
    metricsSummary: (window?: SummaryWindow) => get<MetricsSummary>(`/api/metrics/summary${query({ window })}`),

    updateStatus: () => get<UpdateStatus>('/api/settings/update'),
    checkUpdate: () => post<UpdateStatus>('/api/settings/update/check'),
    metricsTokenStatus: () => get<MetricsTokenStatus>('/api/settings/metrics-token'),
    createMetricsToken: () => post<MetricsToken>('/api/settings/metrics-token'),
    deleteMetricsToken: () => call<void>('DELETE', '/api/settings/metrics-token').then((r) => r.data),
    changePassword: (current: string, next: string) =>
      post<void>('/api/password', { current, new: next } satisfies ChangePasswordRequest),
    metricsHistory: (
      name: string,
      p: { from?: string; to?: string; step?: string; service?: string },
      signal?: AbortSignal,
    ) =>
      call<MetricsHistory>(
        'GET',
        `/api/stacks/${seg(name)}/metrics${query({ from: p.from, to: p.to, step: p.step, service: p.service })}`,
        undefined,
        { signal },
      ).then((r) => r.data),
    healthHistory: (name: string, p?: { span?: string }, signal?: AbortSignal) =>
      call<HealthHistory>(
        'GET',
        `/api/stacks/${seg(name)}/health${query({ span: p?.span })}`,
        undefined,
        { signal },
      ).then((r) => r.data),
    ...createImagesApi(base),
  };
}

export type Api = ReturnType<typeof createApi>;

/** Members for this server's own peering and fleet endpoints; the server never proxies them to a connected server. */
function createLocalOnlyApi() {
  const { call, get, post } = helpers('');
  return {
    peers: () => get<PeerView[]>('/api/peers'),
    addPeer: (req: AddPeerRequest) => post<PeerView>('/api/peers', req),
    removePeer: (id: string) => call<void>('DELETE', `/api/peers/${seg(id)}`).then((r) => r.data),
    probePeer: (url: string) => post<PeerProbe>('/api/peers/probe', { url } satisfies PeerProbeRequest),
    createConnectionToken: () => post<ConnectionToken>('/api/peers/tokens'),
    serverSelf: () => get<ServerSelf>('/api/peers/self'),
    updateServerSelf: (body: ServerSelfUpdate) => call<ServerSelf>('PUT', '/api/peers/self', body).then((r) => r.data),
    checkPeer: (id: string) => post<PeerView>(`/api/peers/${seg(id)}/check`),
    fleetStacks: () => get<FleetStacks>('/api/fleet/stacks'),
    ...createLocalImagesApi(),
  };
}

export const api = { ...createApi(''), ...createLocalOnlyApi() };

/** The local `api`: every scoped member plus this server's own peering and fleet endpoints. */
export type LocalApi = typeof api;
