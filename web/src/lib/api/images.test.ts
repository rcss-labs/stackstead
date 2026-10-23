import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { installFakeFetch, type FakeFetch } from '../../vitest-setup';
import { onUnauthorized } from './client';
import { createImagesApi } from './images';

let fake: FakeFetch;
afterEach(() => fake?.restore());
beforeEach(() => onUnauthorized(() => {}));

const revision = { number: 2, createdAt: '2026-10-01T00:00:00Z', summary: 'upload', dockerfileChanged: false };

describe('createImagesApi', () => {
  it('uploads files as multipart form data with f/<path> part names', async () => {
    const bodies: BodyInit[] = [];
    fake = installFakeFetch({ 'POST /api/images/web/files': { status: 201, body: revision } });
    const inner = globalThis.fetch;
    globalThis.fetch = ((i: RequestInfo | URL, o?: RequestInit) => {
      if (o?.body) bodies.push(o.body);
      return inner(i, o);
    }) as typeof fetch;

    await createImagesApi('').uploadImageFiles('web', [{ path: 'src/app.py', blob: new Blob(['print(1)']) }]);

    const form = bodies[0];
    expect(form).toBeInstanceOf(FormData);
    expect([...(form as FormData).keys()]).toEqual(['f/src/app.py']);
    expect(fake.calls[0].headers.get('content-type')).toBeNull();
  });

  it('encodes each path segment of a file but keeps the slashes', async () => {
    fake = installFakeFetch({
      'GET /api/images/web/files/src/my%20file%231.py': { body: { path: 'src/my file#1.py', content: '', revision: 1 } },
    });
    await createImagesApi('').imageFile('web', 'src/my file#1.py');
    expect(fake.calls[0].path).toBe('/api/images/web/files/src/my%20file%231.py');
  });

  it('prefixes every path with the base', async () => {
    fake = installFakeFetch({
      'GET /api/servers/p1/api/images': { body: { images: [], servers: [] } },
      'GET /api/servers/p1/api/images/registries': { body: [] },
    });
    const images = createImagesApi('/api/servers/p1');
    await images.images();
    await images.imageRegistries();
    expect(fake.calls.map((c) => c.path)).toEqual(['/api/servers/p1/api/images', '/api/servers/p1/api/images/registries']);
  });
});

describe('version routes', () => {
  it('reads and deletes a version by its id, and pushes by tag', async () => {
    fake = installFakeFetch({
      'GET /api/images/web/versions/v%2F1': { body: { id: 'v/1', log: '', usedBy: [], unreachable: [] } },
      'DELETE /api/images/web/versions/v-2': { status: 204 },
      'POST /api/images/web/versions/1/push': { body: { id: 'op', stack: '', action: 'push', state: 'running', startedAt: '2026-10-01T00:00:00Z' } },
    });
    const api = createImagesApi('');
    await api.imageVersion('web', 'v/1');
    await api.deleteImageVersion('web', 'v-2');
    await api.pushImageVersion('web', '1', 't1');
    expect(fake.calls.map((c) => `${c.method} ${c.path}`)).toEqual([
      'GET /api/images/web/versions/v%2F1',
      'DELETE /api/images/web/versions/v-2',
      'POST /api/images/web/versions/1/push',
    ]);
  });
});
