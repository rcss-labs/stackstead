import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Library, LibraryImage, LibraryServer, LibraryVersion } from '$lib/api/imageTypes';
import { LibraryPicker } from './LibraryPicker';

afterEach(cleanup);

const local: LibraryServer = { id: '0123456789abcdef', name: 'home', slug: '', local: true, status: 'reachable' };
const peer: LibraryServer = { id: 'fedcba9876543210', name: 'mini', slug: 'mini', local: false, status: 'reachable' };

const version = (tag: string, over: Partial<LibraryVersion> = {}): LibraryVersion => ({
  id: `v-${tag}`,
  tag,
  status: 'succeeded',
  revision: 1,
  commit: '',
  buildArgs: {},
  arch: 'amd64',
  imageId: '',
  size: 0,
  startedAt: '2026-10-20T10:00:00Z',
  error: '',
  operationId: '',
  ref: '',
  pushes: [],
  ...over,
});

// The library list carries each image's latest version only.
const image = (slug: string, owner: LibraryServer, latestVersion?: LibraryVersion): LibraryImage => ({
  slug,
  name: slug,
  source: 'upload',
  dockerfilePath: 'Dockerfile',
  owner,
  buildArgs: {},
  retention: { keepLast: 0, keepDays: 0 },
  pushTargets: [],
  createdAt: '2026-10-20T10:00:00Z',
  updatedAt: '2026-10-20T10:00:00Z',
  latestVersion,
});

const library = (images: LibraryImage[]): Library => ({ images, servers: [local, peer] });

function mount(lib: Library, props: { value?: string; onChange?: (ref: string) => void; localArch?: string; versions?: Record<string, LibraryVersion[]> } = {}) {
  const onChange = props.onChange ?? vi.fn();
  const loadImage = (img: LibraryImage) =>
    Promise.resolve({ ...img, versions: props.versions?.[img.slug] ?? (img.latestVersion ? [img.latestVersion] : []), suggestedTag: '', tagHint: '', arch: '', revision: 1 });
  render(
    <MemoryRouter>
      <LibraryPicker value={props.value ?? ''} onChange={onChange} load={() => Promise.resolve(lib)} loadImage={loadImage} localArch={props.localArch} />
    </MemoryRouter>,
  );
  return onChange;
}

const WEB2 = 'stackstead.invalid/0123456789ab/web:2';

describe('LibraryPicker', () => {
  it('groups images by owner and lists images with a succeeded latest version only', async () => {
    mount(
      library([
        image('web', local, version('2')),
        image('broken', local, version('3', { status: 'failed' })),
        image('unbuilt', local),
        image('api', peer, version('7')),
      ]),
    );
    await screen.findByRole('group', { name: 'home' });
    expect(screen.getByRole('group', { name: 'mini' })).toBeTruthy();
    expect(screen.getByRole('radio', { name: 'web' })).toBeTruthy();
    expect(screen.getByRole('radio', { name: 'api' })).toBeTruthy();
    expect(screen.queryByRole('radio', { name: 'broken' })).toBeNull();
    expect(screen.getAllByRole('radio')).toHaveLength(2);
  });

  it('choosing an image defaults to its latest version and offers every succeeded version, newest first', async () => {
    const onChange = mount(library([image('web', local, version('3', { startedAt: '2026-10-22T10:00:00Z' }))]), {
      versions: {
        web: [
          version('1', { startedAt: '2026-10-20T10:00:00Z' }),
          version('3', { startedAt: '2026-10-22T10:00:00Z' }),
          version('2', { startedAt: '2026-10-21T10:00:00Z' }),
          version('4', { status: 'failed', startedAt: '2026-10-23T10:00:00Z' }),
        ],
      },
    });
    await userEvent.setup().click(await screen.findByRole('radio', { name: 'web' }));
    await waitFor(() => expect(onChange).toHaveBeenCalledWith('stackstead.invalid/0123456789ab/web:3'));
    const versions = within(await screen.findByRole('group', { name: 'Version' }));
    expect(versions.getAllByRole('radio').map((r) => (r as HTMLInputElement).value.split(':').pop())).toEqual(['3', '2', '1']);
  });

  it('calls onChange with the reference of the version chosen', async () => {
    const onChange = mount(library([image('web', local, version('2'))]), { versions: { web: [version('2'), version('1')] }, value: WEB2 });
    await userEvent.setup().click(await screen.findByRole('radio', { name: '1' }));
    expect(onChange).toHaveBeenLastCalledWith('stackstead.invalid/0123456789ab/web:1');
  });

  it('preselects the image and the older version an existing reference points at', async () => {
    mount(library([image('web', local, version('3'))]), {
      value: 'stackstead.invalid/0123456789ab/web:1',
      versions: { web: [version('3', { startedAt: '2026-10-22T10:00:00Z' }), version('1', { startedAt: '2026-10-20T10:00:00Z' }), version('2', { status: 'failed' })] },
    });
    expect(((await screen.findByRole('radio', { name: 'web' })) as HTMLInputElement).checked).toBe(true);
    expect(((await screen.findByRole('radio', { name: '1' })) as HTMLInputElement).checked).toBe(true);
    expect(screen.getAllByRole('radio', { name: /^\d$/ })).toHaveLength(2);
    expect(screen.queryByText(/^Current:/)).toBeNull();
  });

  it('shows Current only for a tag that is no longer among the versions', async () => {
    mount(library([image('web', local, version('3'))]), { value: 'stackstead.invalid/0123456789ab/web:9', versions: { web: [version('3')] } });
    await screen.findByRole('group', { name: 'Version' });
    expect(screen.getByText('Current: stackstead.invalid/0123456789ab/web:9')).toBeTruthy();
  });

  it('disables a version built for another architecture and says why', async () => {
    const onChange = mount(library([image('web', local, version('2'))]), { localArch: 'amd64', value: WEB2, versions: { web: [version('2'), version('1', { arch: 'arm64' })] } });
    const radio = (await screen.findByRole('radio', { name: /^1/ })) as HTMLInputElement;
    expect(radio.disabled).toBe(true);
    expect(screen.getByText('built for arm64; this server is amd64')).toBeTruthy();
    expect((screen.getByRole('radio', { name: '2' }) as HTMLInputElement).disabled).toBe(false);
    await userEvent.setup().click(radio);
    expect(onChange).not.toHaveBeenCalled();
  });

  it('defaults to the newest compatible version when the latest is for another architecture', async () => {
    const onChange = mount(library([image('web', local, version('3', { arch: 'arm64' }))]), {
      localArch: 'amd64',
      versions: { web: [version('3', { arch: 'arm64', startedAt: '2026-10-22T10:00:00Z' }), version('2', { startedAt: '2026-10-21T10:00:00Z' })] },
    });
    await userEvent.setup().click(await screen.findByRole('radio', { name: 'web' }));
    await waitFor(() => expect(onChange).toHaveBeenCalledWith('stackstead.invalid/0123456789ab/web:2'));
    expect(onChange).not.toHaveBeenCalledWith('stackstead.invalid/0123456789ab/web:3');
  });

  it('clears the choice and shows the note when no version is compatible', async () => {
    const onChange = mount(library([image('web', local, version('3', { arch: 'arm64' }))]), { localArch: 'amd64', versions: { web: [version('3', { arch: 'arm64' })] } });
    await userEvent.setup().click(await screen.findByRole('radio', { name: 'web' }));
    await waitFor(() => expect(onChange).toHaveBeenCalledWith(''));
    expect(onChange).not.toHaveBeenCalledWith('stackstead.invalid/0123456789ab/web:3');
    expect(screen.getByText('built for arm64; this server is amd64')).toBeTruthy();
  });

  it('replaces an existing reference to a wrong-architecture version', async () => {
    const onChange = mount(library([image('web', local, version('3'))]), {
      localArch: 'amd64',
      value: 'stackstead.invalid/0123456789ab/web:1',
      versions: { web: [version('3', { startedAt: '2026-10-22T10:00:00Z' }), version('1', { arch: 'arm64', startedAt: '2026-10-20T10:00:00Z' })] },
    });
    await waitFor(() => expect(onChange).toHaveBeenCalledWith('stackstead.invalid/0123456789ab/web:3'));
  });

  it('shows an empty library with a link to the images screen', async () => {
    mount(library([]));
    expect(await screen.findByText('No images yet')).toBeTruthy();
    expect(screen.getByRole('link', { name: /images/i }).getAttribute('href')).toBe('/images');
  });

  it('says so when the library cannot be loaded', async () => {
    render(
      <MemoryRouter>
        <LibraryPicker value="" onChange={() => {}} load={() => Promise.reject(new Error('boom'))} loadImage={() => Promise.reject(new Error('x'))} />
      </MemoryRouter>,
    );
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/could not load/i));
  });
});
