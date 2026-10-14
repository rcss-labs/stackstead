import { get } from 'svelte/store';
import { describe, expect, it, vi, type Mock } from 'vitest';
import { ApiError } from '../api/client';
import type { ServiceConfig, StackDetail } from '../api/types';
import { fromForm, toForm } from '../compose/form';
import { createServiceEdit, type ServiceEditDeps } from './serviceEdit';

const config: ServiceConfig = { image: 'nginx:1', ports: ['80:80'], volumes: ['/a:/b'], limits: {} };

type Deps = ServiceEditDeps & { preview: Mock<ServiceEditDeps['preview']>; update: Mock<ServiceEditDeps['update']> };
function setup(over: Partial<ServiceEditDeps> = {}, readOnly: ('ports' | 'volumes')[] = []) {
  const deps = {
    preview: vi.fn(async (_c, h) => ({ content: 'new: yaml\n', hash: h })),
    update: vi.fn(async () => ({ hash: 'h2' })),
    compose: vi.fn(async () => ({ content: 'old: yaml\n', hash: 'h1' })),
    reloadDetail: vi.fn(async (): Promise<StackDetail> => detail('h9', config)),
    ...over,
  } as Deps;
  const edit = createServiceEdit('web', 'app', { config, readOnly, hash: 'h1' }, deps);
  return { edit, deps };
}
function detail(hash: string, cfg: ServiceConfig, readOnly: ('ports' | 'volumes')[] = []): StackDetail {
  return {
    name: 'web', path: '/s/web', project: 'web', origin: 'managed', status: 'running', serviceCount: 1, running: 1, hash,
    services: [{ name: 'app', config: cfg, readOnly, containers: [] }],
  };
}
const setImage = (edit: ReturnType<typeof setup>['edit'], image: string) => edit.form.update((f) => ({ ...f, image }));

describe('createServiceEdit', () => {
  it('validation errors block the preview without any request', async () => {
    const { edit, deps } = setup();
    setImage(edit, '');
    await edit.preview();
    expect(get(edit.errors).image).toBeTruthy();
    expect(get(edit.phase)).toBe('editing');
    expect(deps.preview).not.toHaveBeenCalled();
    expect(deps.compose).not.toHaveBeenCalled();
  });

  it('preview sends fromForm(form, original) with the loaded hash and exposes current vs candidate', async () => {
    const { edit, deps } = setup();
    setImage(edit, 'nginx:2');
    await edit.preview();
    expect(deps.preview).toHaveBeenCalledWith(fromForm(get(edit.form), config), 'h1');
    expect(deps.preview.mock.calls[0][0].image).toBe('nginx:2');
    expect(get(edit.phase)).toBe('previewing');
    expect(get(edit.previewText)).toEqual({ original: 'old: yaml\n', modified: 'new: yaml\n' });
  });

  it('TestPreviewAndSaveUseSameHash', async () => {
    const { edit, deps } = setup();
    setImage(edit, 'nginx:2');
    await edit.preview();
    expect(await edit.save()).toBe(true);
    expect(deps.update).toHaveBeenCalledOnce();
    expect(deps.update.mock.calls[0][1]).toBe(deps.preview.mock.calls[0][1]);
    expect(deps.update.mock.calls[0][0]).toEqual(deps.preview.mock.calls[0][0]);
    expect(get(edit.phase)).toBe('saved');
    expect(get(edit.hash)).toBe('h2');
    expect(get(edit.dirty)).toBe(false);
  });

  it('save reports partial checks', async () => {
    const { edit } = setup({ update: vi.fn(async () => ({ hash: 'h2', checksPartial: true })) });
    setImage(edit, 'nginx:2');
    await edit.preview();
    await edit.save();
    expect(get(edit.checksPartial)).toBe(true);
  });

  it('a compose hash that differs from the detail hash reloads the detail first and previews with the new hash', async () => {
    let n = 0;
    const { edit, deps } = setup({
      compose: vi.fn(async () => (++n === 1 ? { content: 'x', hash: 'h9' } : { content: 'old: yaml\n', hash: 'h9' })),
    });
    setImage(edit, 'nginx:2');
    await edit.preview();
    expect(deps.reloadDetail).toHaveBeenCalledOnce();
    expect(deps.preview.mock.calls[0][1]).toBe('h9');
    expect(get(edit.form).image).toBe('nginx:2');
  });

  it('400 field errors land on form paths and return to editing', async () => {
    const { edit } = setup({
      preview: vi.fn(async () => {
        throw new ApiError(400, 'invalid', [{ field: 'services.app.ports[0]', message: 'port taken' }]);
      }),
    });
    setImage(edit, 'nginx:2');
    await edit.preview();
    expect(get(edit.errors)['ports[0]']).toBe('port taken');
    expect(get(edit.phase)).toBe('editing');
  });

  it('422 "needs the raw YAML editor" becomes a raw-editor notice, form untouched', async () => {
    const { edit } = setup({
      update: vi.fn(async () => {
        throw new ApiError(422, 'this change needs the raw YAML editor');
      }),
    });
    setImage(edit, 'nginx:2');
    await edit.preview();
    await edit.save();
    expect(get(edit.notice)).toEqual({ kind: 'rawEditor', message: 'this change needs the raw YAML editor' });
    expect(get(edit.form).image).toBe('nginx:2');
    expect(get(edit.phase)).toBe('editing');
  });

  it('other 422 shows an error notice', async () => {
    const { edit } = setup({
      preview: vi.fn(async () => {
        throw new ApiError(422, 'compose rejected it');
      }),
    });
    setImage(edit, 'nginx:2');
    await edit.preview();
    expect(get(edit.notice)).toEqual({ kind: 'error', message: 'compose rejected it' });
  });

  it('409 enters conflict; reload(detail) keeps the user form values and takes the new hash', async () => {
    let fileHash = 'h1';
    const { edit, deps } = setup({
      compose: vi.fn(async () => ({ content: 'old: yaml\n', hash: fileHash })),
      update: vi.fn(async () => {
        throw new ApiError(409, 'compose file changed on disk');
      }),
    });
    setImage(edit, 'nginx:2');
    await edit.preview();
    expect(await edit.save()).toBe(false);
    fileHash = 'h9';
    expect(get(edit.phase)).toBe('conflict');
    expect(get(edit.conflictMessage)).toBe('compose file changed on disk');
    edit.reload(detail('h9', { ...config, restart: 'always' }));
    expect(get(edit.phase)).toBe('editing');
    expect(get(edit.hash)).toBe('h9');
    expect(get(edit.form).image).toBe('nginx:2');
    expect(get(edit.dirty)).toBe(true);
    await edit.preview();
    expect(deps.preview.mock.calls.at(-1)![1]).toBe('h9');
  });

  it('discard reseeds the form from the fresh detail', async () => {
    const { edit } = setup();
    setImage(edit, 'nginx:2');
    edit.discard(detail('h9', { ...config, image: 'nginx:3' }));
    expect(get(edit.form).image).toBe('nginx:3');
    expect(get(edit.hash)).toBe('h9');
    expect(get(edit.dirty)).toBe(false);
  });

  it('read-only groups are sent unchanged', async () => {
    const { edit, deps } = setup({}, ['ports']);
    expect(get(edit.form)).toEqual(toForm(config, ['ports']));
    setImage(edit, 'nginx:2');
    await edit.preview();
    expect(deps.preview.mock.calls[0][0].ports).toEqual(config.ports);
  });
});
