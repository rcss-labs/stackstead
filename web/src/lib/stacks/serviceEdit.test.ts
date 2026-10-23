import { get } from '$lib/store';
import { describe, expect, it, vi, type Mock } from 'vitest';
import { ApiError } from '../api/client';
import type { DockerfileView, ServiceConfig, StackDetail } from '../api/types';
import { fromForm, toForm } from '../compose/form';
import { createServiceEdit, type ServiceEditDeps } from './serviceEdit';

const config: ServiceConfig = { image: 'nginx:1', ports: ['80:80'], volumes: ['/a:/b'], limits: {} };

type Deps = ServiceEditDeps & { preview: Mock<ServiceEditDeps['preview']>; update: Mock<ServiceEditDeps['update']> };
function setup(over: Partial<ServiceEditDeps> = {}, readOnly: ('ports' | 'volumes')[] = [], cfg: ServiceConfig = config) {
  const deps = {
    preview: vi.fn(async (_c, h) => ({ content: 'new: yaml\n', hash: h })),
    update: vi.fn(async () => ({ hash: 'h2' })),
    compose: vi.fn(async () => ({ content: 'old: yaml\n', hash: 'h1' })),
    reloadDetail: vi.fn(async (): Promise<StackDetail> => detail('h9', config)),
    ...over,
  } as Deps;
  const edit = createServiceEdit('app', { config: cfg, readOnly, hash: 'h1' }, deps);
  return { edit, deps };
}
function detail(hash: string, cfg: ServiceConfig, readOnly: ('ports' | 'volumes')[] = []): StackDetail {
  return {
    name: 'web', path: '/s/web', project: 'web', origin: 'managed', status: 'running', serviceCount: 1, running: 1, health: { healthy: 0, unhealthy: 0, starting: 0, none: 0, failed: 0 }, ports: [], hash,
    libraryUpdates: 0,
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

  const NOTICE = 'The file changed since you opened this editor';

  it('a concurrent change to an untouched group is kept when the user edited only the image', async () => {
    const base: ServiceConfig = { ...config, limits: { memory: '256M' } };
    let fileHash = 'h9';
    const { edit, deps } = setup(
      {
        compose: vi.fn(async () => ({ content: 'old: yaml\n', hash: fileHash })),
        reloadDetail: vi.fn(async () => {
          fileHash = 'h9';
          return detail('h9', { ...base, limits: { memory: '1G' } });
        }),
      },
      [],
      base,
    );
    setImage(edit, 'nginx:2');
    await edit.preview();
    const sent = deps.preview.mock.calls[0][0];
    expect(sent.limits.memory).toBe('1G');
    expect(sent.image).toBe('nginx:2');
    expect(deps.preview.mock.calls[0][1]).toBe('h9');
    expect(get(edit.rebased)).toBe(true);
  });

  it('a group edited by both keeps the user value and flags the rebase', async () => {
    let fileHash = 'h9';
    const { edit, deps } = setup({
      compose: vi.fn(async () => ({ content: 'old: yaml\n', hash: fileHash })),
      reloadDetail: vi.fn(async () => {
        fileHash = 'h9';
        return detail('h9', { ...config, image: 'nginx:9' });
      }),
    });
    setImage(edit, 'nginx:2');
    await edit.preview();
    expect(deps.preview.mock.calls[0][0].image).toBe('nginx:2');
    expect(get(edit.rebased)).toBe(true);
    edit.cancelPreview();
    expect(NOTICE).toBeTruthy();
  });

  it('conflict reload applies only the groups the user changed on top of the new file', async () => {
    const { edit } = setup({
      update: vi.fn(async () => {
        throw new ApiError(409, 'changed');
      }),
    });
    setImage(edit, 'nginx:2');
    await edit.preview();
    await edit.save();
    edit.reload(detail('h9', { ...config, image: 'nginx:9', restart: 'always' }));
    expect(get(edit.form).image).toBe('nginx:2');
    expect(get(edit.form).restart).toBe('always');
    expect(get(edit.rebased)).toBe(true);
    expect(get(edit.dirty)).toBe(true);
  });

  it('reload re-applies the new read-only set and takes the new file values for those groups', async () => {
    let fileHash = 'h1';
    const { edit, deps } = setup({
      compose: vi.fn(async () => ({ content: 'old: yaml\n', hash: fileHash })),
      update: vi.fn(async () => {
        throw new ApiError(409, 'changed');
      }),
    });
    edit.form.update((f) => ({ ...f, image: 'nginx:2', ports: ['81:81'] }));
    await edit.preview();
    await edit.save();
    fileHash = 'h9';
    edit.reload(detail('h9', { ...config, ports: ['8080:80'] }, ['ports']));
    expect(get(edit.form).readOnly.has('ports')).toBe(true);
    expect(get(edit.form).ports).toEqual(['8080:80']);
    await edit.preview();
    expect(deps.preview.mock.calls.at(-1)![0].ports).toEqual(['8080:80']);
    expect(deps.preview.mock.calls.at(-1)![0].image).toBe('nginx:2');
  });

  it('reload names the groups whose edits were dropped because they became read-only', async () => {
    const { edit } = setup({
      update: vi.fn(async () => {
        throw new ApiError(409, 'changed');
      }),
    });
    edit.form.update((f) => ({ ...f, image: 'nginx:2', ports: ['81:81'] }));
    await edit.preview();
    await edit.save();
    edit.reload(detail('h9', { ...config, ports: ['8080:80'] }, ['ports']));
    expect(get(edit.discardedGroups)).toEqual(['Ports']);
    edit.discard(detail('h9', config));
    expect(get(edit.discardedGroups)).toEqual([]);
  });

  it('a second preview while one is in flight starts no second chain', async () => {
    let release!: () => void;
    const { edit, deps } = setup({
      compose: vi.fn(() => new Promise<{ content: string; hash: string }>((r) => (release = () => r({ content: 'old', hash: 'h1' })))),
    });
    setImage(edit, 'nginx:2');
    const first = edit.preview();
    expect(get(edit.phase)).toBe('busy');
    const second = edit.preview();
    release();
    await Promise.all([first, second]);
    expect(deps.compose).toHaveBeenCalledOnce();
    expect(deps.preview).toHaveBeenCalledOnce();
  });

  it('edits typed while the save is in flight stay dirty afterwards', async () => {
    let release!: () => void;
    const { edit } = setup({
      update: vi.fn(() => new Promise<{ hash: string }>((r) => (release = () => r({ hash: 'h2' })))),
    });
    setImage(edit, 'nginx:2');
    await edit.preview();
    const saving = edit.save();
    setImage(edit, 'nginx:3');
    release();
    await saving;
    expect(get(edit.dirty)).toBe(true);
    expect(get(edit.phase)).toBe('editing');
  });

  it('a 422 about raw YAML (read-only field) routes to the raw editor', async () => {
    const { edit } = setup({
      preview: vi.fn(async () => {
        throw new ApiError(422, 'ports can only be edited as raw YAML');
      }),
    });
    setImage(edit, 'nginx:2');
    await edit.preview();
    expect(get(edit.notice)?.kind).toBe('rawEditor');
  });
});

describe('createServiceEdit with a Dockerfile', () => {
  const buildConfig: ServiceConfig = { image: 'stackstead/web:latest', build: { context: './app', dockerfile: 'Dockerfile.dev' }, limits: {} };
  const view = (over: Partial<DockerfileView> = {}): DockerfileView => ({ content: 'FROM alpine:3.20\n', hash: 'd1', exists: true, editable: true, path: 'Dockerfile.dev', ...over });
  const withView = (over: Partial<DockerfileView> = {}, deps: Partial<ServiceEditDeps> = {}, cfg: ServiceConfig = buildConfig) =>
    setup({ loadDockerfile: vi.fn(async () => view(over)), ...deps }, [], cfg);

  it('loads an editable Dockerfile as the source', async () => {
    const { edit } = withView();
    await vi.waitFor(() => expect(get(edit.form).source).toBe('dockerfile'));
    const f = get(edit.form);
    expect(f.dockerfileText).toBe('FROM alpine:3.20\n');
    expect(f.dockerfileAt).toEqual({ context: './app', dockerfile: 'Dockerfile.dev' });
    expect(get(edit.dirty)).toBe(false);
  });

  it('keeps the build form editable when the Dockerfile is not editable, and can switch to a managed Dockerfile', async () => {
    const loadDockerfile = vi.fn(async () => view({ editable: false, content: '', reason: 'outside' }));
    const { edit, deps } = setup({ loadDockerfile }, [], buildConfig);
    await vi.waitFor(() => expect(get(edit.dockerfileView)?.editable).toBe(false));
    const f = get(edit.form);
    expect(f.source).toBe('build');
    expect(f.readOnly.has('build')).toBe(false);
    // Editing the existing build fields works as today.
    edit.form.update((x) => ({ ...x, buildContext: './other' }));
    await edit.preview();
    expect(deps.preview.mock.calls[0][0].build).toEqual({ context: './other', dockerfile: 'Dockerfile.dev' });
    edit.cancelPreview();
    // Switching to Write a Dockerfile uses the managed layout.
    edit.form.update((x) => ({ ...x, source: 'dockerfile', dockerfileText: 'FROM alpine:3.21\n' }));
    await edit.preview();
    expect(deps.preview.mock.calls[1][0].build).toEqual({ context: './app' });
    expect(await edit.save()).toBe(true);
    expect(deps.update.mock.calls[0][2]).toEqual({ content: 'FROM alpine:3.21\n', hash: '' });
  });

  it('seeds an editable but empty Dockerfile as Existing build so other fields can be saved', async () => {
    const loadDockerfile = vi.fn(async () => view({ content: '' }));
    const { edit, deps } = setup({ loadDockerfile }, [], buildConfig);
    await vi.waitFor(() => expect(loadDockerfile).toHaveBeenCalled());
    await vi.waitFor(() => expect(get(edit.dockerfileView)).toBeDefined());
    expect(get(edit.form).source).toBe('build');
    edit.form.update((f) => ({ ...f, ports: ['81:81'] }));
    await edit.preview();
    expect(deps.preview).toHaveBeenCalled();
    expect(await edit.save()).toBe(true);
    expect(deps.update.mock.calls[0][2]).toBeUndefined();
  });

  it('does not load a Dockerfile for an image service', () => {
    const loadDockerfile = vi.fn(async () => view());
    setup({ loadDockerfile });
    expect(loadDockerfile).not.toHaveBeenCalled();
  });

  it('saving a Dockerfile change sends content and the loaded hash', async () => {
    const { edit, deps } = withView();
    await vi.waitFor(() => expect(get(edit.form).source).toBe('dockerfile'));
    edit.form.update((f) => ({ ...f, dockerfileText: 'FROM alpine:3.21\n' }));
    await edit.preview();
    expect(deps.preview.mock.calls[0]).toHaveLength(2);
    expect(await edit.save()).toBe(true);
    expect(deps.update).toHaveBeenCalledWith(expect.objectContaining({ build: { context: './app', dockerfile: 'Dockerfile.dev' } }), 'h1', { content: 'FROM alpine:3.21\n', hash: 'd1' });
  });

  describe('the Dockerfile baseline after a save', () => {
    async function saveTwice(updateResult: { hash: string; dockerfileHash?: string }, loadDockerfile: Mock<() => Promise<DockerfileView>>) {
      let composeHash = 'h1';
      const update = vi.fn<ServiceEditDeps['update']>(async () => {
        composeHash = updateResult.hash;
        return updateResult;
      });
      const { edit, deps } = setup({ loadDockerfile, update, compose: vi.fn(async () => ({ content: 'old: yaml\n', hash: composeHash })) }, [], buildConfig);
      await vi.waitFor(() => expect(get(edit.form).source).toBe('dockerfile'));
      edit.form.update((f) => ({ ...f, dockerfileText: 'FROM alpine:3.21\n' }));
      await edit.preview();
      expect(await edit.save()).toBe(true);
      edit.form.update((f) => ({ ...f, dockerfileText: 'FROM alpine:3.22\n' }));
      await edit.preview();
      await edit.save();
      return { deps, update };
    }

    it('uses the hash returned by the write and does not fetch the Dockerfile again, so another writer cannot slip in', async () => {
      // Whatever a re-fetch would return is another writer's version D3.
      const loadDockerfile = vi.fn(async () => view());
      loadDockerfile.mockResolvedValueOnce(view());
      const { update } = await saveTwice({ hash: 'h2', dockerfileHash: 'd2' }, loadDockerfile);
      expect(loadDockerfile).toHaveBeenCalledTimes(1);
      expect(update.mock.calls[1][2]).toEqual({ content: 'FROM alpine:3.22\n', hash: 'd2' });
    });

    it('without a returned hash (older server) never adopts a re-fetched hash for text it did not see', async () => {
      const loadDockerfile = vi.fn(async () => view());
      loadDockerfile.mockResolvedValueOnce(view());
      loadDockerfile.mockResolvedValueOnce(view({ content: 'FROM other-writer\n', hash: 'd3' }));
      const { update } = await saveTwice({ hash: 'h2' }, loadDockerfile);
      expect(update.mock.calls[1][2]?.hash).not.toBe('d3');
    });

    it('without a returned hash adopts the re-fetched hash when the file holds exactly the saved text', async () => {
      const loadDockerfile = vi.fn(async () => view());
      loadDockerfile.mockResolvedValueOnce(view());
      loadDockerfile.mockResolvedValueOnce(view({ content: 'FROM alpine:3.21\n', hash: 'd2' }));
      const { update } = await saveTwice({ hash: 'h2' }, loadDockerfile);
      expect(update.mock.calls[1][2]?.hash).toBe('d2');
    });
  });

  it('does not send the Dockerfile when only another group changed', async () => {
    const { edit, deps } = withView();
    await vi.waitFor(() => expect(get(edit.form).source).toBe('dockerfile'));
    edit.form.update((f) => ({ ...f, ports: ['81:81'] }));
    await edit.preview();
    await edit.save();
    expect(deps.update.mock.calls[0][2]).toBeUndefined();
  });

  it('switching an image service to a written Dockerfile sends hash ""', async () => {
    const { edit, deps } = setup();
    edit.form.update((f) => ({ ...f, source: 'dockerfile', dockerfileText: 'FROM alpine:3.20\n', image: 'stackstead/web-app:latest' }));
    await edit.preview();
    expect(await edit.save()).toBe(true);
    expect(deps.update).toHaveBeenCalledWith(expect.objectContaining({ build: { context: './app' } }), 'h1', { content: 'FROM alpine:3.20\n', hash: '' });
  });

  it('a Dockerfile conflict enters the conflict phase and Reload fetches the view again', async () => {
    const loadDockerfile = vi.fn(async () => view());
    const update = vi.fn(async () => {
      throw new ApiError(409, 'Dockerfile changed on disk since it was loaded', []);
    });
    const { edit } = setup({ loadDockerfile, update }, [], buildConfig);
    await vi.waitFor(() => expect(get(edit.form).source).toBe('dockerfile'));
    edit.form.update((f) => ({ ...f, dockerfileText: 'FROM busybox:1\n' }));
    await edit.preview();
    expect(await edit.save()).toBe(false);
    expect(get(edit.phase)).toBe('conflict');
    expect(get(edit.conflictMessage)).toBe('Dockerfile changed on disk since it was loaded');
    loadDockerfile.mockResolvedValueOnce(view({ content: 'FROM alpine:3.22\n', hash: 'd2' }));
    await edit.reload(detail('h1', buildConfig));
    expect(loadDockerfile).toHaveBeenCalledTimes(2);
    expect(get(edit.form).dockerfileText).toBe('FROM busybox:1\n');
    expect(get(edit.phase)).toBe('editing');
  });

  it('maps a server dockerfile field error to the editor', async () => {
    const update = vi.fn(async () => {
      throw new ApiError(400, 'invalid', [{ field: 'dockerfile', message: 'Dockerfile is not valid' }]);
    });
    const { edit } = withView({}, { update });
    await vi.waitFor(() => expect(get(edit.form).source).toBe('dockerfile'));
    edit.form.update((f) => ({ ...f, dockerfileText: 'x' }));
    await edit.preview();
    await edit.save();
    expect(get(edit.errors).dockerfileText).toBe('Dockerfile is not valid');
  });

  it('Reload keeps the Dockerfile source and the user text when the re-fetch fails', async () => {
    const loadDockerfile = vi.fn(async () => view());
    const { edit } = setup({ loadDockerfile }, [], buildConfig);
    await vi.waitFor(() => expect(get(edit.form).source).toBe('dockerfile'));
    edit.form.update((f) => ({ ...f, dockerfileText: 'FROM busybox:1\n' }));
    loadDockerfile.mockRejectedValueOnce(new Error('down'));
    await edit.reload(detail('h1', buildConfig));
    expect(get(edit.form).source).toBe('dockerfile');
    expect(get(edit.form).dockerfileText).toBe('FROM busybox:1\n');
    expect(get(edit.dockerfileView)?.hash).toBe('d1');
  });

  it('Discard keeps the Dockerfile source when the re-fetch fails', async () => {
    const loadDockerfile = vi.fn(async () => view());
    const { edit } = setup({ loadDockerfile }, [], buildConfig);
    await vi.waitFor(() => expect(get(edit.form).source).toBe('dockerfile'));
    loadDockerfile.mockRejectedValueOnce(new Error('down'));
    await edit.discard(detail('h1', buildConfig));
    expect(get(edit.form).source).toBe('dockerfile');
    expect(get(edit.form).dockerfileText).toBe('FROM alpine:3.20\n');
  });

  it('ignores a late first load once the user has started a save', async () => {
    let release!: (v: DockerfileView) => void;
    const loadDockerfile = vi.fn(() => new Promise<DockerfileView>((r) => (release = r)));
    const { edit } = setup({ loadDockerfile }, [], buildConfig);
    edit.form.update((f) => ({ ...f, ports: ['81:81'] }));
    await edit.preview();
    expect(get(edit.phase)).toBe('previewing');
    release(view());
    await new Promise((r) => setTimeout(r, 0));
    expect(get(edit.form).source).toBe('build');
  });

  it('flags a Dockerfile conflict so the banner can say the text will replace it', async () => {
    const update = vi.fn(async () => {
      throw new ApiError(409, 'Dockerfile changed on disk since it was loaded', []);
    });
    const { edit } = withView({}, { update });
    await vi.waitFor(() => expect(get(edit.form).source).toBe('dockerfile'));
    edit.form.update((f) => ({ ...f, dockerfileText: 'x' }));
    await edit.preview();
    await edit.save();
    expect(get(edit.dockerfileConflict)).toBe(true);
  });
});
