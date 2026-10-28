import { render, screen, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it } from 'vitest';
import type { BackupArchive } from '$lib/api/backupTypes';
import { installFakeFetch, type FakeFetch } from '../../vitest-setup';
import { ServerContext, localScope } from '../servers/ServerContext';
import { BackupsScreen } from './BackupsScreen';

let fake: FakeFetch;
afterEach(() => fake.restore());

const archive = (hostSlug: string, name: string): BackupArchive => ({
  dest: 'local',
  name,
  hostSlug,
  createdAt: '2026-10-26T03:00:00Z',
  size: 1024,
  encrypted: false,
});

describe('BackupsScreen', () => {
  it('reads archives through the peer base', async () => {
    fake = installFakeFetch({ 'GET /api/servers/p1/api/backups/archives': { body: [] } });
    render(
      <MemoryRouter>
        <ServerContext.Provider value={{ ...localScope, local: false, id: 'p1', slug: 'p1' }}>
          <BackupsScreen />
        </ServerContext.Provider>
      </MemoryRouter>,
    );
    await screen.findByText(/No archives/);
    expect(fake.calls.some((c) => c.path === '/api/servers/p1/api/backups/archives')).toBe(true);
  });

  it('identifies this host through the peer, not by its URL slug', async () => {
    fake = installFakeFetch({
      'GET /api/servers/p1/api/backups/archives': { body: [archive('alpha', 'a1.tar.gz'), archive('identity', 'i1.tar.gz')] },
      'GET /api/servers/p1/api/peers/self': { body: { name: 'P', slug: 'identity' } },
    });
    render(
      <MemoryRouter>
        <ServerContext.Provider value={{ ...localScope, local: false, id: 'p1', slug: 'url-slug' }}>
          <BackupsScreen />
        </ServerContext.Provider>
      </MemoryRouter>,
    );
    await screen.findByText('i1.tar.gz');
    const headings = screen.getAllByRole('heading', { level: 2 }).map((h) => h.textContent);
    expect(headings.slice(0, 2)).toEqual(['identity (this host)', 'alpha']);
  });

  it('groups archives by host with this host first', async () => {
    fake = installFakeFetch({
      'GET /api/backups/archives': { body: [archive('alpha', 'a1.tar.gz'), archive('mine', 'm1.tar.gz')] },
      'GET /api/peers/self': { body: { name: 'Mine', slug: 'mine' } },
    });
    render(
      <MemoryRouter>
        <BackupsScreen />
      </MemoryRouter>,
    );
    await screen.findByText('m1.tar.gz');
    const headings = screen.getAllByRole('heading', { level: 2 }).map((h) => h.textContent);
    expect(headings.slice(0, 2)).toEqual(['mine (this host)', 'alpha']);
    expect(screen.getAllByRole('button', { name: 'Restore' })[0].getAttribute('aria-disabled')).toBeNull();
  });

  it('tags each archive as encrypted or unencrypted', async () => {
    fake = installFakeFetch({
      'GET /api/backups/archives': {
        body: [
          { ...archive('mine', 'm1.tar.gz.enc'), encrypted: true },
          { ...archive('mine', 'm0.tar.gz'), encrypted: false },
        ],
      },
      'GET /api/peers/self': { body: { name: 'Mine', slug: 'mine' } },
    });
    render(
      <MemoryRouter>
        <BackupsScreen />
      </MemoryRouter>,
    );
    await screen.findByText('m1.tar.gz.enc');
    const row = (name: string) => screen.getByText(name).closest('tr')!;
    expect(within(row('m1.tar.gz.enc')).getByText('Encrypted')).toBeTruthy();
    expect(within(row('m0.tar.gz')).getByText('Unencrypted')).toBeTruthy();
  });
});
