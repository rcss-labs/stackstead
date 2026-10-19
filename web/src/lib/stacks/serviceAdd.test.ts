import { get } from '$lib/store';
import { describe, expect, it, vi } from 'vitest';
import { ApiError } from '../api/client';
import type { StackDetail } from '../api/types';
import { createServiceAdd, type ServiceAddDeps } from './serviceAdd';
const detail = (hash = 'h2') => ({ name: 'web', hash, services: [] }) as unknown as StackDetail;
function deferred<T>() {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>(r => resolve = r);
  return { promise, resolve };
}
function setup(over: Partial<ServiceAddDeps> = {}) {
  const deps = { compose: vi.fn(async () => ({ content: 'old', hash: 'h1' })), reloadDetail: vi.fn(async () => detail()), preview: vi.fn(async (_s, _c, hash) => ({ content: 'new', hash })), add: vi.fn(async () => ({ detail: detail(), checksPartial: true })), ...over } satisfies ServiceAddDeps;
  const add = createServiceAdd('h1', deps);
  add.name.set('cache');
  add.form.update(f => ({ ...f, image: 'redis:7' }));
  return { add, deps };
}
describe('service add', () => {
  it('validates before any request', async () => {
    const { add, deps } = setup();
    add.name.set('');
    await add.preview();
    expect(get(add.errors).name).toBeTruthy();
    expect(deps.compose).not.toHaveBeenCalled();
  });
  it('rereads matching compose content after reloading a moved hash', async () => {
    const compose = vi.fn().mockResolvedValueOnce({ content: 'stale', hash: 'h2' }).mockResolvedValueOnce({ content: 'current', hash: 'h2' });
    const { add, deps } = setup({ compose });
    await add.preview();
    expect(deps.reloadDetail).toHaveBeenCalledOnce();
    expect(deps.preview).toHaveBeenCalledWith('cache', expect.objectContaining({ image: 'redis:7' }), 'h2');
    expect(get(add.previewText)?.original).toBe('current');
  });
  it('refuses a file that moves during reload and mismatched preview hashes', async () => {
    const { add, deps } = setup({ compose: vi.fn(async () => ({ content: 'wrong', hash: 'h3' })) });
    await add.preview();
    expect(get(add.phase)).toBe('conflict');
    expect(deps.preview).not.toHaveBeenCalled();
    const other = setup({ preview: vi.fn(async () => ({ content: 'wrong', hash: 'other' })) }).add;
    await other.preview();
    expect(get(other.phase)).toBe('conflict');
    expect(await other.save()).toBeUndefined();
  });
  it('saves exactly the reviewed service/config/hash despite edits during preview', async () => {
    const gate = deferred<{
      content: string;
      hash: string;
    }>();
    const { add, deps } = setup({ preview: vi.fn(() => gate.promise) });
    const p = add.preview();
    await vi.waitFor(() => expect(deps.preview).toHaveBeenCalled());
    add.name.set('changed');
    add.form.update(f => ({ ...f, image: 'redis:8' }));
    gate.resolve({ content: 'new', hash: 'h1' });
    await p;
    expect(await add.save()).toEqual(detail());
    expect(deps.add).toHaveBeenCalledWith('cache', expect.objectContaining({ image: 'redis:7' }), 'h1');
    expect(get(add.checksPartial)).toBe(true);
  });
  it('maps service errors and handles conflict reload without losing edits', async () => {
    const { add } = setup({ preview: vi.fn(async () => {
        throw new ApiError(400, 'bad', [{ field: 'service', message: 'taken' }]);
      }) });
    await add.preview();
    expect(get(add.errors).name).toBe('taken');
    const next = setup({ add: vi.fn(async () => {
        throw new ApiError(409, 'changed on disk');
      }) }).add;
    await next.preview();
    await next.save();
    expect(get(next.phase)).toBe('conflict');
    next.reload(detail('h9'));
    expect(get(next.hash)).toBe('h9');
    expect(get(next.name)).toBe('cache');
    expect(get(next.form).image).toBe('redis:7');
  });
  it('blocks duplicate preview/save chains and ignores late completions after discard', async () => {
    const gate = deferred<{
      content: string;
      hash: string;
    }>();
    const { add, deps } = setup({ preview: vi.fn(() => gate.promise) });
    const p = add.preview();
    await add.preview();
    await vi.waitFor(() => expect(deps.preview).toHaveBeenCalledOnce());
    add.discard(detail());
    gate.resolve({ content: 'late', hash: 'h1' });
    await p;
    expect(get(add.phase)).toBe('editing');
    expect(get(add.previewText)).toBeUndefined();
    expect(get(add.dirty)).toBe(false);
    const saveGate = deferred<{
      detail: StackDetail;
      checksPartial: boolean;
    }>();
    const b = setup({ add: vi.fn(() => saveGate.promise) });
    await b.add.preview();
    const saving = b.add.save();
    expect(await b.add.save()).toBeUndefined();
    expect(b.deps.add).toHaveBeenCalledOnce();
    b.add.dispose();
    saveGate.resolve({ detail: detail(), checksPartial: false });
    expect(await saving).toBeUndefined();
  });
});
