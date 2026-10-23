import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { filesOf, imageDetail, installImagesBackend, mountImages, type ImagesHarness } from './imagesTestKit';

// Keep the plain textarea: the CodeMirror chunk is exercised in the YamlEditor and DiffView tests.
vi.mock('$lib/stacks/cm', () => ({
  createEditor: () => {
    throw new Error('chunk unavailable in this test');
  },
  createDiffView: () => {
    throw new Error('chunk unavailable in this test');
  },
}));

let backend: ImagesHarness;
afterEach(() => backend.restore());

const IMG = '/api/images/web';
const REVISION = { number: 5, createdAt: '2026-10-23T10:00:00Z', summary: 'x', dockerfileChanged: true };
const editor = () => screen.findByRole('textbox', { name: 'Dockerfile' }) as Promise<HTMLTextAreaElement>;

function install(extra: Parameters<typeof installImagesBackend>[0] = {}) {
  backend = installImagesBackend({
    [`GET ${IMG}`]: { body: imageDetail() },
    [`GET ${IMG}/dockerfile`]: { body: { content: 'FROM alpine\n', revision: 4 } },
    [`GET ${IMG}/files`]: { body: filesOf() },
    ...extra,
  });
}

/** Records the multipart bodies the page sends. */
function captureForms(): FormData[] {
  const forms: FormData[] = [];
  const inner = globalThis.fetch;
  globalThis.fetch = ((input: RequestInfo | URL, init?: RequestInit) => {
    if (init?.body instanceof FormData) forms.push(init.body);
    return inner(input, init);
  }) as typeof fetch;
  return forms;
}

describe('Dockerfile editor', () => {
  it('loads the Dockerfile and saves content with the revision it loaded', async () => {
    install({ [`PUT ${IMG}/dockerfile`]: { body: REVISION } });
    mountImages('/images/web?tab=source');
    const ta = await editor();
    expect(ta.value).toBe('FROM alpine\n');
    fireEvent.change(ta, { target: { value: 'FROM alpine:3.20\n' } });
    await userEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(backend.calls('PUT', `${IMG}/dockerfile`)).toHaveLength(1));
    expect(backend.calls('PUT', `${IMG}/dockerfile`)[0].body).toEqual({ content: 'FROM alpine:3.20\n', revision: 4 });
  });

  it('a revision conflict says the file changed and offers Reload', async () => {
    install({ [`PUT ${IMG}/dockerfile`]: { status: 409, body: { error: 'revision mismatch', code: 'revision_conflict' } } });
    mountImages('/images/web?tab=source');
    fireEvent.change(await editor(), { target: { value: 'FROM busybox\n' } });
    await userEvent.click(screen.getByRole('button', { name: 'Save' }));
    expect(await screen.findByText(/changed since you loaded it/)).toBeTruthy();
    const before = backend.calls('GET', `${IMG}/dockerfile`).length;
    await userEvent.click(screen.getByRole('button', { name: 'Reload' }));
    await waitFor(() => expect(backend.calls('GET', `${IMG}/dockerfile`).length).toBe(before + 1));
  });
});

describe('File tree', () => {
  it('lists files under their folders', async () => {
    install();
    mountImages('/images/web?tab=source');
    const tree = await screen.findByRole('tree', { name: 'Build context files' });
    expect(within(tree).getByText('app')).toBeTruthy();
    expect(within(tree).getByText('main.sh')).toBeTruthy();
    expect(within(tree).getByText('Dockerfile')).toBeTruthy();
  });

  it('uploads picked files under their names', async () => {
    install({ [`POST ${IMG}/files`]: { body: REVISION } });
    const forms = captureForms();
    mountImages('/images/web?tab=source');
    const input = await screen.findByLabelText('Upload files');
    await userEvent.upload(input, new File(['echo hi'], 'run.sh'));
    await waitFor(() => expect(forms).toHaveLength(1));
    expect([...forms[0].keys()]).toEqual(['f/run.sh']);
  });

  it('uploads a picked folder with relative paths', async () => {
    install({ [`POST ${IMG}/files`]: { body: REVISION } });
    const forms = captureForms();
    mountImages('/images/web?tab=source');
    const input = await screen.findByLabelText('Upload folder');
    const file = new File(['x'], 'a.txt');
    Object.defineProperty(file, 'webkitRelativePath', { value: 'assets/a.txt' });
    await userEvent.upload(input, file);
    await waitFor(() => expect(forms).toHaveLength(1));
    expect([...forms[0].keys()]).toEqual(['f/assets/a.txt']);
  });

  it('renames a file and refreshes the list', async () => {
    install({ [`POST ${IMG}/files/rename`]: { body: REVISION } });
    mountImages('/images/web?tab=source');
    await userEvent.click(await screen.findByRole('button', { name: 'Rename app/main.sh' }));
    const dialog = await screen.findByRole('dialog', { name: 'Rename file' });
    const field = within(dialog).getByLabelText('New path');
    expect((field as HTMLInputElement).value).toBe('app/main.sh');
    await userEvent.clear(field);
    await userEvent.type(field, 'app/start.sh');
    const before = backend.calls('GET', `${IMG}/files`).length;
    await userEvent.click(within(dialog).getByRole('button', { name: 'Rename' }));
    await waitFor(() => expect(backend.calls('POST', `${IMG}/files/rename`)).toHaveLength(1));
    expect(backend.calls('POST', `${IMG}/files/rename`)[0].body).toEqual({ from: 'app/main.sh', to: 'app/start.sh' });
    await waitFor(() => expect(backend.calls('GET', `${IMG}/files`).length).toBe(before + 1));
  });

  it('deletes a file after confirmation and refreshes the list', async () => {
    install({ [`DELETE ${IMG}/files/app/main.sh`]: { body: REVISION } });
    mountImages('/images/web?tab=source');
    await userEvent.click(await screen.findByRole('button', { name: 'Delete app/main.sh' }));
    const dialog = await screen.findByRole('alertdialog');
    expect(backend.calls('DELETE', `${IMG}/files/app/main.sh`)).toHaveLength(0);
    const before = backend.calls('GET', `${IMG}/files`).length;
    await userEvent.click(within(dialog).getByRole('button', { name: 'Delete file' }));
    await waitFor(() => expect(backend.calls('DELETE', `${IMG}/files/app/main.sh`)).toHaveLength(1));
    await waitFor(() => expect(backend.calls('GET', `${IMG}/files`).length).toBe(before + 1));
  });
});
