import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { installFakeFetch, type FakeFetch } from '../../vitest-setup';
import { ApiError, api, onUnauthorized, request } from './client';

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

  it('sends {} for body-less POSTs such as act', async () => {
    fake = installFakeFetch({ 'POST /api/stacks/web/actions/up': { status: 202, body: { id: '1' } } });
    await api.act('web', 'up');
    expect(fake.calls[0].body).toEqual({});
    expect(fake.calls[0].headers.get('content-type')).toBe('application/json');
  });

  it('sends {} for cancel and createMetricsToken', async () => {
    fake = installFakeFetch({
      'POST /api/operations/7/cancel': { status: 204 },
      'POST /api/settings/metrics-token': { body: { token: 't' } },
    });
    await api.cancel('7');
    await api.createMetricsToken();
    expect(fake.calls.map((c) => c.body)).toEqual([{}, {}]);
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
      'GET /api/operations?state=running': { body: [] },
    });
    await api.fs('/srv/a b');
    await api.stack('my stack');
    await api.metricsHistory('web', { from: '1', to: '2', step: '30s', service: 'api' });
    await api.runningOperations();
    expect(fake.calls.map((c) => c.path)).toEqual([
      '/api/fs?path=%2Fsrv%2Fa+b',
      '/api/stacks/my%20stack',
      '/api/stacks/web/metrics?from=1&to=2&step=30s&service=api',
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
      'POST /api/login': { status: 401, body: { error: 'bad credentials' } },
      'GET /api/setup/status': { status: 401, body: { error: 'x' } },
      'POST /api/setup': { status: 401, body: { error: 'x' } },
      'GET /api/me': { status: 401, body: { error: 'x' } },
    });
    await Promise.allSettled([api.login('a', 'b'), api.setupStatus(), api.setup('a', 'b'), api.me()]);
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
