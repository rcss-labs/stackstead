import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { installFakeFetch, type FakeFetch } from '../../vitest-setup';
import { ApiError, api, createApi, onUnauthorized, request } from './client';

let fake: FakeFetch;
afterEach(() => fake?.restore());

describe('request', () => {
  beforeEach(() => onUnauthorized(() => {}));

  it('sends JSON body, headers and same-origin credentials on POST', async () => {
    const init: RequestInit[] = [];
    fake = installFakeFetch({ 'POST /api/login': { body: { username: 'a' } } });
    const orig = globalThis.fetch;
    globalThis.fetch = ((i: RequestInfo | URL, o?: RequestInit) => {
      init.push(o ?? {});
      return orig(i, o);
    }) as typeof fetch;
    await request('POST', '/api/login', { username: 'a', password: 'b' });
    expect(fake.calls[0].body).toEqual({ username: 'a', password: 'b' });
    expect(fake.calls[0].headers.get('content-type')).toBe('application/json');
    expect(fake.calls[0].headers.get('accept')).toBe('application/json');
    expect(init[0].credentials).toBe('same-origin');
    expect(init[0].redirect).toBe('error');
  });

  it('passes the abort signal to fetch', async () => {
    fake = installFakeFetch({ 'GET /api/stacks': { body: [] } });
    const ac = new AbortController();
    await request('GET', '/api/stacks', undefined, { signal: ac.signal });
    expect(fake.calls[0].signal).toBe(ac.signal);
  });

  it('rejects a 2xx with a non-JSON body', async () => {
    fake = installFakeFetch({
      'GET /api/stacks': { body: 'not json', headers: { 'content-type': 'text/plain' } },
    });
    const err = await request('GET', '/api/stacks').catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err.message).toBe('invalid response');
  });

  it('rethrows the abort reason when the body read is aborted', async () => {
    fake = installFakeFetch({});
    const ac = new AbortController();
    const reason = new Error('custom reason');
    globalThis.fetch = (async () => {
      const body = new ReadableStream({
        start(c) {
          ac.signal.addEventListener('abort', () => c.error(reason));
        },
      });
      return new Response(body, { status: 200 });
    }) as typeof fetch;
    const p = request('GET', '/api/stacks', undefined, { signal: ac.signal }).catch((e) => e);
    ac.abort(reason);
    expect(await p).toBe(reason);
  });

  it('rethrows a custom abort reason from fetch instead of network error', async () => {
    fake = installFakeFetch({});
    const ac = new AbortController();
    const reason = new Error('custom');
    ac.abort(reason);
    globalThis.fetch = (() => Promise.reject(new TypeError('x'))) as typeof fetch;
    const err = await request('GET', '/api/stacks', undefined, { signal: ac.signal }).catch((e) => e);
    expect(err).toBe(reason);
  });

  it('sends {} for body-less POSTs such as act', async () => {
    fake = installFakeFetch({ 'POST /api/stacks/web/actions/up': { status: 202, body: { id: '1' } } });
    await api.act('web', 'up');
    expect(fake.calls[0].body).toEqual({});
    expect(fake.calls[0].headers.get('content-type')).toBe('application/json');
  });

  it('sends {} for cancel and createMetricsToken', async () => {
    fake = installFakeFetch({
      'POST /api/operations/7/cancel': { status: 202, body: { id: '7' } },
      'POST /api/settings/metrics-token': { body: { token: 't' } },
    });
    await api.cancel('7');
    await api.createMetricsToken();
    expect(fake.calls.map((c) => c.body)).toEqual([{}, {}]);
  });

  it('updateStatus GETs /api/settings/update', async () => {
    fake = installFakeFetch({ 'GET /api/settings/update': { body: { state: 'unknown' } } });
    expect(await api.updateStatus()).toEqual({ state: 'unknown' });
    expect(fake.calls[0].method).toBe('GET');
  });

  it('checkUpdate POSTs /api/settings/update/check with {}', async () => {
    fake = installFakeFetch({ 'POST /api/settings/update/check': { body: { state: 'up-to-date' } } });
    expect(await api.checkUpdate()).toEqual({ state: 'up-to-date' });
    expect(fake.calls[0].body).toEqual({});
    expect(fake.calls[0].headers.get('content-type')).toBe('application/json');
  });

  it('sends no body and no content-type on DELETE', async () => {
    fake = installFakeFetch({ 'DELETE /api/stacks/web?volumes=true': { status: 202, body: { id: '1' } } });
    await api.remove('web', true);
    expect(fake.calls[0].method).toBe('DELETE');
    expect(fake.calls[0].body).toBeUndefined();
    expect(fake.calls[0].headers.has('content-type')).toBe(false);
  });

  it('maps error bodies to ApiError with fields', async () => {
    fake = installFakeFetch({
      'PUT /api/stacks/web/compose': {
        status: 400,
        body: { error: 'invalid', fields: [{ field: 'image', message: 'required' }] },
      },
    });
    const err = await request('PUT', '/api/stacks/web/compose', {}).catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(400);
    expect(err.message).toBe('invalid');
    expect(err.fields).toEqual([{ field: 'image', message: 'required' }]);
  });

  it('falls back to HTTP <status> for non-JSON error bodies', async () => {
    fake = installFakeFetch({
      'GET /api/stacks': { status: 500, body: '<html>boom</html>', headers: { 'content-type': 'text/html' } },
    });
    const err = await request('GET', '/api/stacks').catch((e) => e);
    expect(err.message).toBe('HTTP 500');
    expect(err.fields).toEqual([]);
  });

  it('returns undefined data on 204', async () => {
    fake = installFakeFetch({ 'POST /api/logout': { status: 204 } });
    const res = await request('POST', '/api/logout', {});
    expect(res.data).toBeUndefined();
    expect(res.checksPartial).toBe(false);
  });

  it('reports checksPartial from X-Stackstead-Checks', async () => {
    fake = installFakeFetch({
      'POST /api/stacks': { status: 201, body: {}, headers: { 'X-Stackstead-Checks': 'partial' } },
    });
    expect((await request('POST', '/api/stacks', {})).checksPartial).toBe(true);
  });

  it('turns network failures into ApiError status 0', async () => {
    fake = installFakeFetch({});
    globalThis.fetch = (() => Promise.reject(new TypeError('Failed to fetch'))) as typeof fetch;
    const err = await request('GET', '/api/stacks').catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(0);
    expect(err.message).toBe('network error');
  });

  it('lets aborts propagate untouched', async () => {
    fake = installFakeFetch({});
    const ac = new AbortController();
    globalThis.fetch = (() => Promise.reject(new DOMException('aborted', 'AbortError'))) as typeof fetch;
    ac.abort();
    const err = await request('GET', '/api/stacks', undefined, { signal: ac.signal }).catch((e) => e);
    expect(err.name).toBe('AbortError');
  });
});

describe('api wrappers', () => {
  beforeEach(() => onUnauthorized(() => {}));

  it('encodes paths and queries', async () => {
    fake = installFakeFetch({
      'GET /api/fs?path=%2Fsrv%2Fa+b': { body: [] },
      'GET /api/stacks/my%20stack': { body: {} },
      'GET /api/stacks/web/metrics?from=1&to=2&step=30s&service=api': { body: { points: [] } },
      'GET /api/stacks/web/health?span=30m': { body: { from: 0, to: 0, services: [] } },
      'GET /api/stacks/web/health': { body: { from: 0, to: 0, services: [] } },
      'GET /api/operations?state=running': { body: [] },
    });
    await api.fs('/srv/a b');
    await api.stack('my stack');
    await api.metricsHistory('web', { from: '1', to: '2', step: '30s', service: 'api' });
    await api.healthHistory('web', { span: '30m' });
    await api.healthHistory('web');
    await api.runningOperations();
    expect(fake.calls.map((c) => c.path)).toEqual([
      '/api/fs?path=%2Fsrv%2Fa+b',
      '/api/stacks/my%20stack',
      '/api/stacks/web/metrics?from=1&to=2&step=30s&service=api',
      '/api/stacks/web/health?span=30m',
      '/api/stacks/web/health',
      '/api/operations?state=running',
    ]);
  });

  it('sends the hash with saves and previews', async () => {
    fake = installFakeFetch({
      'PUT /api/stacks/web/services/api': { body: { hash: 'h2' } },
      'POST /api/stacks/web/services/api/preview': { body: { content: 'x', hash: 'h1' } },
      'PUT /api/stacks/web/compose': { body: { hash: 'h3' } },
    });
    const config = { limits: {} };
    await api.updateService('web', 'api', config, 'h1');
    await api.previewService('web', 'api', config, 'h1');
    await api.saveCompose('web', 'yaml', 'h1');
    expect(fake.calls.map((c) => c.body)).toEqual([
      { config, hash: 'h1' },
      { config, hash: 'h1' },
      { content: 'yaml', hash: 'h1' },
    ]);
  });

  const serviceDetail = {
    name: 'my stack', path: '/srv/my stack', project: 'my-stack', origin: 'managed', status: 'stopped',
    serviceCount: 1, running: 0, health: { healthy: 0, unhealthy: 0, starting: 0, none: 0, failed: 0 },
    ports: [], hash: 'h2',
    services: [{ name: 'api/worker', config: { image: 'alpine:3.21', limits: {} }, readOnly: [], containers: [] }],
  };

  it.each([false, true])('adds a service and surfaces checksPartial=%s with the detail', async (partial) => {
    fake = installFakeFetch({
      'POST /api/stacks/my%20stack/services': {
        status: 201, body: serviceDetail, headers: partial ? { 'X-Stackstead-Checks': 'partial' } : {},
      },
    });
    const config = { image: 'alpine:3.21', limits: {} };
    expect(await api.addService('my stack', 'api/worker', config, 'h1')).toEqual({
      data: serviceDetail, checksPartial: partial,
    });
    expect(fake.calls).toHaveLength(1);
    expect(fake.calls[0]).toMatchObject({
      method: 'POST', path: '/api/stacks/my%20stack/services', body: { service: 'api/worker', config, hash: 'h1' },
    });
  });

  it('addService sends dockerfile only when given', async () => {
    fake = installFakeFetch({
      'POST /api/stacks/web/services': { status: 201, body: serviceDetail },
    });
    const config = { image: 'alpine:3.21', limits: {} };
    await api.addService('web', 'api', config, 'h1');
    await api.addService('web', 'api', config, 'h1', { content: 'FROM x' });
    expect(fake.calls[0].body).toEqual({ service: 'api', config, hash: 'h1' });
    expect(Object.keys(fake.calls[0].body as object)).not.toContain('dockerfile');
    expect(fake.calls[1].body).toEqual({ service: 'api', config, hash: 'h1', dockerfile: { content: 'FROM x' } });
  });

  it('updateService sends dockerfile with hash', async () => {
    fake = installFakeFetch({ 'PUT /api/stacks/web/services/api': { body: { hash: 'h2' } } });
    const config = { limits: {} };
    await api.updateService('web', 'api', config, 'h1');
    await api.updateService('web', 'api', config, 'h1', { content: 'FROM y', hash: 'd1' });
    expect(fake.calls[0].body).toEqual({ config, hash: 'h1' });
    expect(fake.calls[1].body).toEqual({ config, hash: 'h1', dockerfile: { content: 'FROM y', hash: 'd1' } });
  });

  it('dockerfile() GETs the service dockerfile URL', async () => {
    const view = { content: 'FROM x', hash: 'd1', exists: true, editable: true, path: 'services/web/Dockerfile' };
    fake = installFakeFetch({ 'GET /api/stacks/a%20b/services/web/dockerfile': { body: view } });
    expect(await api.dockerfile('a b', 'web')).toEqual(view);
    expect(fake.calls[0].path).toBe('/api/stacks/a%20b/services/web/dockerfile');
  });

  it('saveDockerfile PUTs {content, hash}', async () => {
    fake = installFakeFetch({ 'PUT /api/stacks/web/services/api/dockerfile': { body: { hash: 'd2' } } });
    expect(await api.saveDockerfile('web', 'api', 'FROM z', 'd1')).toEqual({ hash: 'd2' });
    expect(fake.calls[0]).toMatchObject({
      method: 'PUT', path: '/api/stacks/web/services/api/dockerfile', body: { content: 'FROM z', hash: 'd1' },
    });
  });

  it('upService POSTs {} to the scoped up action', async () => {
    const op = { id: 'op1', stack: 'web', action: 'up', service: 'api', state: 'running', startedAt: '2026-10-20T00:00:00Z' };
    fake = installFakeFetch({ 'POST /api/stacks/web/services/api/actions/up': { status: 202, body: op } });
    expect(await api.upService('web', 'api')).toEqual(op);
    expect(fake.calls[0]).toMatchObject({
      method: 'POST', path: '/api/stacks/web/services/api/actions/up', body: {},
    });
  });

  it('previews an added service with its name, config and hash', async () => {
    const preview = { content: 'services: {}\n', hash: 'h1' };
    fake = installFakeFetch({ 'POST /api/stacks/my%20stack/services/preview': { body: preview } });
    const config = { image: 'alpine:3.21', limits: {} };
    expect(await api.previewAddService('my stack', 'api/worker', config, 'h1')).toEqual(preview);
    expect(fake.calls).toHaveLength(1);
    expect(fake.calls[0]).toMatchObject({
      method: 'POST', path: '/api/stacks/my%20stack/services/preview', body: { service: 'api/worker', config, hash: 'h1' },
    });
  });

  it('removes a service with encoded path segments and hash query and returns detail', async () => {
    fake = installFakeFetch({
      'DELETE /api/stacks/my%20stack/services/api%2Fworker?hash=h%2B%2F%3D%26%3F%23': { body: serviceDetail },
    });
    expect(await api.removeService('my stack', 'api/worker', 'h+/=&?#')).toEqual(serviceDetail);
    expect(fake.calls).toHaveLength(1);
    expect(fake.calls[0]).toMatchObject({
      method: 'DELETE', path: '/api/stacks/my%20stack/services/api%2Fworker?hash=h%2B%2F%3D%26%3F%23', body: undefined,
    });
  });

  it('sends setup token only when given and password change fields', async () => {
    fake = installFakeFetch({ 'POST /api/setup': { status: 204 }, 'POST /api/password': { status: 204 } });
    await api.setup('u', 'p');
    await api.setup('u', 'p', 'tok');
    await api.changePassword('old', 'new');
    expect(fake.calls.map((c) => c.body)).toEqual([
      { username: 'u', password: 'p' },
      { username: 'u', password: 'p', setupToken: 'tok' },
      { current: 'old', new: 'new' },
    ]);
  });
});

describe('401 handling', () => {
  it('TestUnauthorizedRoutesToLoginOnce: concurrent 401s call the handler once', async () => {
    const handler = vi.fn();
    onUnauthorized(handler);
    fake = installFakeFetch({
      'GET /api/stacks': { status: 401, body: { error: 'unauthorized' } },
      'GET /api/networks': { status: 401, body: { error: 'unauthorized' } },
    });
    const results = await Promise.allSettled([api.stacks(), api.networks()]);
    expect(results.every((r) => r.status === 'rejected')).toBe(true);
    expect((results[0] as PromiseRejectedResult).reason.status).toBe(401);
    expect(handler).toHaveBeenCalledTimes(1);
  });

  it('does not trigger on login, setup or me', async () => {
    const handler = vi.fn();
    onUnauthorized(handler);
    fake = installFakeFetch({
      'POST /api/logout': { status: 401, body: { error: 'x' } },
      'POST /api/login': { status: 401, body: { error: 'bad credentials' } },
      'GET /api/setup/status': { status: 401, body: { error: 'x' } },
      'POST /api/setup': { status: 401, body: { error: 'x' } },
      'GET /api/me': { status: 401, body: { error: 'x' } },
    });
    await Promise.allSettled([api.login('a', 'b'), api.logout(), api.setupStatus(), api.setup('a', 'b'), api.me()]);
    expect(handler).not.toHaveBeenCalled();
  });

  it('never triggers on 403 (wrong current password)', async () => {
    const handler = vi.fn();
    onUnauthorized(handler);
    fake = installFakeFetch({ 'POST /api/password': { status: 403, body: { error: 'wrong password' } } });
    const err = await api.changePassword('x', 'y').catch((e) => e);
    expect(err.status).toBe(403);
    expect(handler).not.toHaveBeenCalled();
  });
});

describe('dashboard endpoints', () => {
  beforeEach(() => onUnauthorized(() => {}));

  it('requests host, alerts and the summary window', async () => {
    fake = installFakeFetch({
      'GET /api/host': { body: {} },
      'GET /api/alerts': { body: [] },
      'GET /api/metrics/summary': { body: {} },
      'GET /api/metrics/summary?window=1h': { body: {} },
    });
    await api.host();
    await api.alerts();
    await api.metricsSummary();
    await api.metricsSummary('1h');
    expect(fake.calls.map((c) => c.path)).toEqual([
      '/api/host',
      '/api/alerts',
      '/api/metrics/summary',
      '/api/metrics/summary?window=1h',
    ]);
  });

  it('surfaces a 400 as ApiError', async () => {
    fake = installFakeFetch({ 'GET /api/metrics/summary?window=1h': { status: 400, body: { error: 'bad window' } } });
    const err = await api.metricsSummary('1h').catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(400);
  });
});


describe('container endpoints', () => {
  beforeEach(() => onUnauthorized(() => {}));

  it('lists, reads and acts on containers', async () => {
    fake = installFakeFetch({
      'GET /api/containers': { body: [] },
      'GET /api/containers/abc123def456': { body: { id: 'abc123def456' } },
      'POST /api/containers/abc123def456/start': { body: { id: 'abc123def456' } },
      'POST /api/containers/abc123def456/stop': { body: {} },
      'POST /api/containers/abc123def456/restart': { body: {} },
    });
    await api.containers();
    await api.container('abc123def456');
    await api.containerAction('abc123def456', 'start');
    await api.containerAction('abc123def456', 'stop');
    await api.containerAction('abc123def456', 'restart');
    expect(fake.calls.map((c) => `${c.method} ${c.path}`)).toEqual([
      'GET /api/containers',
      'GET /api/containers/abc123def456',
      'POST /api/containers/abc123def456/start',
      'POST /api/containers/abc123def456/stop',
      'POST /api/containers/abc123def456/restart',
    ]);
    expect(fake.calls[2].body).toEqual({});
  });

  it('encodes the id as one path segment', async () => {
    fake = installFakeFetch({ 'GET /api/containers/a%2Fb': { body: {} } });
    await api.container('a/b');
    expect(fake.calls[0].path).toBe('/api/containers/a%2Fb');
  });
});

describe('network endpoints', () => {
  it('creates a network with the JSON request body and returns the row', async () => {
    const body = { name: 'lan', driver: 'macvlan' as const, subnet: '10.0.0.0/24', gateway: '10.0.0.1', parent: 'eth0', internal: false };
    const row = { ...body, id: 'abc123' };
    fake = installFakeFetch({ 'POST /api/networks': { status: 201, body: row } });
    expect(await api.createNetwork(body)).toEqual(row);
    expect(fake.calls[0].body).toEqual(body);
    expect(fake.calls[0].headers.get('content-type')).toBe('application/json');
  });
  it('deletes an encoded network id without a body and resolves on 204', async () => {
    fake = installFakeFetch({ 'DELETE /api/networks/lan%2Fname%20%3F': { status: 204 } });
    expect(await api.deleteNetwork('lan/name ?')).toBeUndefined();
    expect(fake.calls[0].body).toBeUndefined();
    expect(fake.calls[0].headers.has('content-type')).toBe(false);
  });
  it('gets host interfaces', async () => {
    const rows = [{ name: 'eth0', ipv4: '10.0.0.2', subnet: '10.0.0.0/24', gateway: '', default: true }];
    fake = installFakeFetch({ 'GET /api/host/interfaces': { body: rows } });
    expect(await api.hostInterfaces()).toEqual(rows);
    expect(fake.calls[0].body).toBeUndefined();
  });
});

it('uses storage wrapper routes and action-log filters; keeps refresh rate-limit body', async () => {
  fake = installFakeFetch({
    'GET /api/storage': { body: { images: {} } },
    'GET /api/storage/images': { body: { images: [], totals: {} } },
    'GET /api/storage/volumes': { body: { volumes: [], totals: {} } },
    'GET /api/storage/build-cache': { body: { records: [], totals: {} } },
    'GET /api/actions?kind=storage.&limit=20': { body: [] },
    'POST /api/storage/refresh': { status: 429, body: { error: 'Wait for the current reading', retryAfter: 42 } },
  });
  expect(await api.storageOverview()).toEqual({ images: {} });
  expect(await api.storageImages()).toEqual({ images: [], totals: {} });
  expect(await api.storageVolumes()).toEqual({ volumes: [], totals: {} });
  expect(await api.storageBuildCache()).toEqual({ records: [], totals: {} });
  expect(await api.recentActions({ kind: 'storage.', limit: 20 })).toEqual([]);
  await expect(api.refreshStorage()).rejects.toMatchObject({ status: 429, message: 'Wait for the current reading', body: { retryAfter: 42 } });
  expect(fake.calls.at(-1)?.body).toEqual({});
});

describe('createApi', () => {
  beforeEach(() => onUnauthorized(() => {}));

  it('prefixes every path with the base', async () => {
    fake = installFakeFetch({
      'GET /api/servers/b1/api/stacks': { body: [] },
      'GET /api/servers/b1/api/stacks/a%20b': { body: { name: 'a b' } },
      'POST /api/servers/b1/api/stacks/a%20b/actions/up': { body: { id: 'op' } },
      'POST /api/servers/b1/api/stacks/a%20b/services/web/actions/up': { body: { id: 'op2' } },
    });
    const scoped = createApi('/api/servers/b1');
    await scoped.stacks();
    await scoped.stack('a b');
    await scoped.act('a b', 'up');
    await scoped.upService('a b', 'web');
    expect(fake.calls.map((c) => `${c.method} ${c.path}`)).toEqual([
      'GET /api/servers/b1/api/stacks',
      'GET /api/servers/b1/api/stacks/a%20b',
      'POST /api/servers/b1/api/stacks/a%20b/actions/up',
      'POST /api/servers/b1/api/stacks/a%20b/services/web/actions/up',
    ]);
  });

  it('keeps the default api unprefixed', async () => {
    fake = installFakeFetch({ 'GET /api/stacks': { body: [] } });
    await api.stacks();
    expect(fake.calls[0].path).toBe('/api/stacks');
  });

  it('exposes the error code of a peer failure', async () => {
    fake = installFakeFetch({
      'GET /api/servers/b1/api/stacks': {
        status: 502,
        body: { error: "Can't reach mini-02", code: 'peer_unreachable' },
      },
    });
    const err = await createApi('/api/servers/b1').stacks().catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err.code).toBe('peer_unreachable');
    expect(err.status).toBe(502);
    expect(err.message).toBe("Can't reach mini-02");
  });

  it('leaves the code undefined when the body has none', async () => {
    fake = installFakeFetch({ 'GET /api/stacks': { status: 500, body: { error: 'boom' } } });
    const err = await api.stacks().catch((e) => e);
    expect(err.code).toBeUndefined();
  });

  it('a 502 peer error does not trigger the unauthorized handler', async () => {
    const handler = vi.fn();
    onUnauthorized(handler);
    fake = installFakeFetch({
      'GET /api/servers/b1/api/stacks': { status: 502, body: { error: 'unpaired', code: 'peer_unpaired' } },
    });
    await createApi('/api/servers/b1').stacks().catch(() => {});
    expect(handler).not.toHaveBeenCalled();
  });

  it('a 401 under /api/servers/ triggers the unauthorized handler', async () => {
    const handler = vi.fn();
    onUnauthorized(handler);
    fake = installFakeFetch({
      'GET /api/servers/b1/api/stacks': { status: 401, body: { error: 'unauthorized' } },
    });
    await createApi('/api/servers/b1').stacks().catch(() => {});
    expect(handler).toHaveBeenCalledTimes(1);
  });

  it('calls the local-only peer endpoints', async () => {
    fake = installFakeFetch({
      'GET /api/peers': { body: [] },
      'POST /api/peers': { status: 201, body: {} },
      'DELETE /api/peers/p%201': { status: 204 },
      'POST /api/peers/probe': { body: {} },
      'POST /api/peers/tokens': { body: {} },
      'GET /api/peers/self': { body: {} },
      'PUT /api/peers/self': { body: {} },
      'POST /api/peers/p1/check': { body: {} },
      'GET /api/fleet/stacks': { body: {} },
    });
    const trust = { caPem: '', pinSha256: '', plaintext: false };
    await api.peers();
    await api.addPeer({ url: 'u', token: 't', trust, selfTrust: trust, advertisedUrl: 'a' });
    await api.removePeer('p 1');
    await api.probePeer('https://x');
    await api.createConnectionToken();
    await api.serverSelf();
    await api.updateServerSelf({ name: 'n', advertisedUrl: 'a' });
    await api.checkPeer('p1');
    await api.fleetStacks();
    expect(fake.calls.map((c) => `${c.method} ${c.path}`)).toHaveLength(9);
    expect(fake.calls[3].body).toEqual({ url: 'https://x' });
    expect(fake.calls[6].body).toEqual({ name: 'n', advertisedUrl: 'a' });
  });
});
