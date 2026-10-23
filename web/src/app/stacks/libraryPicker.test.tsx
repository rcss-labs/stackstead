import { cleanup, render, screen, waitFor } from '@testing-library/react';
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

function mount(lib: Library, props: { value?: string; onChange?: (ref: string) => void; localArch?: string } = {}) {
  const onChange = props.onChange ?? vi.fn();
  render(
    <MemoryRouter>
      <LibraryPicker value={props.value ?? ''} onChange={onChange} load={() => Promise.resolve(lib)} localArch={props.localArch} />
    </MemoryRouter>,
  );
  return onChange;
}

describe('LibraryPicker', () => {
  it('groups images by owner and lists succeeded versions only', async () => {
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
    expect(screen.getByRole('radio', { name: /web:2/ })).toBeTruthy();
    expect(screen.queryByRole('radio', { name: /broken:3/ })).toBeNull();
    expect(screen.getAllByRole('radio')).toHaveLength(2);
    expect(screen.getByRole('radio', { name: /api:7/ })).toBeTruthy();
  });

  it('calls onChange with the formatted reference of the chosen version', async () => {
    const onChange = mount(library([image('web', local, version('2'))]));
    await userEvent.setup().click(await screen.findByRole('radio', { name: /web:2/ }));
    expect(onChange).toHaveBeenCalledWith('stackstead.invalid/0123456789ab/web:2');
  });

  it('marks the version matching the value as selected', async () => {
    mount(library([image('web', local, version('2'))]), { value: 'stackstead.invalid/0123456789ab/web:2' });
    const radio = (await screen.findByRole('radio', { name: /web:2/ })) as HTMLInputElement;
    expect(radio.checked).toBe(true);
  });

  it('disables a version built for another architecture and says why', async () => {
    const onChange = mount(library([image('web', local, version('2', { arch: 'arm64' })), image('api', local, version('1'))]), { localArch: 'amd64' });
    const radio = (await screen.findByRole('radio', { name: /web:2/ })) as HTMLInputElement;
    expect(radio.disabled).toBe(true);
    expect(screen.getByText('built for arm64; this server is amd64')).toBeTruthy();
    expect((screen.getByRole('radio', { name: /api:1/ }) as HTMLInputElement).disabled).toBe(false);
    await userEvent.setup().click(radio);
    expect(onChange).not.toHaveBeenCalled();
  });

  it('shows an empty library with a link to the images screen', async () => {
    mount(library([]));
    expect(await screen.findByText('No images yet')).toBeTruthy();
    expect(screen.getByRole('link', { name: /images/i }).getAttribute('href')).toBe('/images');
  });

  it('says so when the library cannot be loaded', async () => {
    render(
      <MemoryRouter>
        <LibraryPicker value="" onChange={() => {}} load={() => Promise.reject(new Error('boom'))} />
      </MemoryRouter>,
    );
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/could not load/i));
  });
});
