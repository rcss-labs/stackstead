import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { imageDetail, installImagesBackend, mountImages, type ImagesHarness } from './imagesTestKit';

const diffs = vi.hoisted(() => [] as { original: string; modified: string }[]);
vi.mock('$lib/stacks/cm', () => ({
  createEditor: () => {
    throw new Error('chunk unavailable in this test');
  },
  createDiffView: (opts: { original: string; modified: string }) => {
    diffs.push({ original: opts.original, modified: opts.modified });
    return { destroy() {} };
  },
}));

let backend: ImagesHarness;
afterEach(() => {
  diffs.length = 0;
  return backend.restore();
});

const IMG = '/api/images/web';
const REVS = [
  { number: 3, createdAt: '2026-10-22T10:00:00Z', summary: 'Edited Dockerfile', dockerfileChanged: true },
  { number: 2, createdAt: '2026-10-21T10:00:00Z', summary: 'Uploaded 2 files', dockerfileChanged: false },
];

describe('Revisions tab', () => {
  it('lists revisions, diffs the selected one against its previous Dockerfile, and restores', async () => {
    backend = installImagesBackend({
      [`GET ${IMG}`]: { body: imageDetail() },
      [`GET ${IMG}/revisions`]: { body: REVS },
      [`GET ${IMG}/revisions/3`]: { body: { ...REVS[0], dockerfile: 'FROM alpine:3.20\n', previous: 'FROM alpine\n', files: [{ path: 'Dockerfile', size: 17 }] } },
      [`POST ${IMG}/revisions/3/restore`]: { body: { ...REVS[0], number: 4 } },
    });
    mountImages('/images/web?tab=revisions');
    const list = await screen.findByRole('list', { name: 'Revisions' });
    expect(within(list).getByText('Edited Dockerfile')).toBeTruthy();
    expect(within(list).getByText('Uploaded 2 files')).toBeTruthy();

    await userEvent.click(within(list).getByRole('button', { name: /Revision 3/ }));
    await waitFor(() => expect(diffs).toContainEqual({ original: 'FROM alpine\n', modified: 'FROM alpine:3.20\n' }));

    const before = backend.calls('GET', `${IMG}/revisions`).length;
    await userEvent.click(screen.getByRole('button', { name: 'Restore revision 3' }));
    await waitFor(() => expect(backend.calls('POST', `${IMG}/revisions/3/restore`)).toHaveLength(1));
    await waitFor(() => expect(backend.calls('GET', `${IMG}/revisions`).length).toBe(before + 1));
  });
});
