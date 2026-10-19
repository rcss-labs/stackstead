import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { get } from '$lib/store';
import { toasts } from '$lib/stores/toasts';
import { installFakeFetch, type FakeFetch, type FakeResponse, type RecordedCall } from '../../vitest-setup';

// Keep the plain textarea: the CodeMirror chunk is exercised in the YamlEditor and DiffView tests.
vi.mock('$lib/stacks/cm', () => ({
  createEditor: () => {
    throw new Error('chunk unavailable in this test');
  },
  createDiffView: () => {
    throw new Error('chunk unavailable in this test');
  },
}));

import { ComposeEditor, type ComposeEditorProps } from './ComposeEditor';

let fake: FakeFetch;
let putResponses: FakeResponse[];
let getResponses: FakeResponse[];

beforeEach(() => {
  toasts.reset();
  getResponses = [{ body: { content: 'a: 1\n', hash: 'h1' } }];
  putResponses = [{ body: { hash: 'h2' } }];
  fake = installFakeFetch({
    'GET /api/stacks/web/compose': () => (getResponses.length > 1 ? getResponses.shift()! : getResponses[0]),
    'PUT /api/stacks/web/compose': () => putResponses.shift() ?? { body: { hash: 'hX' } },
  });
});
afterEach(async () => {
  cleanup();
  fake.restore();
  await new Promise((r) => setTimeout(r, 5)); // useStore releases its subscriptions on the next task
});

const puts = (): RecordedCall[] => fake.calls.filter((c) => c.method === 'PUT');
const textbox = () => screen.getByRole('textbox', { name: /compose file/i }) as HTMLTextAreaElement;
async function loaded(props: Partial<ComposeEditorProps> = {}) {
  const r = render(<ComposeEditor stackName="web" {...props} />);
  const ta = (await screen.findByRole('textbox', { name: /compose file/i })) as HTMLTextAreaElement;
  await waitFor(() => expect(ta.value).toBe('a: 1\n'));
  return { ...r, ta };
}
const edit = (ta: HTMLTextAreaElement, v: string) => fireEvent.change(ta, { target: { value: v } });
const click = (name: string) => userEvent.click(screen.getByRole('button', { name }));

describe('ComposeEditor', () => {
  it('toolbar shows the file name and modified state', async () => {
    const { ta } = await loaded({ composePath: '/srv/stacks/web/compose.yaml' });
    expect(screen.getByText('compose.yaml')).toBeTruthy();
    expect(screen.queryByText('· modified')).toBeNull();
    expect(screen.getByText('⌘S / Ctrl+S to save')).toBeTruthy();
    edit(ta, 'a: 2\n');
    expect(await screen.findByText('· modified')).toBeTruthy();
    expect(screen.getByText(/previous version as/).textContent).toContain('compose.yaml.bak');
  });

  it('a failed initial load shows the error with Retry, not a spinner', async () => {
    getResponses = [{ status: 500, body: { error: 'disk on fire' } }];
    render(<ComposeEditor stackName="web" />);
    expect((await screen.findByRole('alert')).textContent).toContain('disk on fire');
    expect(screen.queryByText(/Loading compose file/)).toBeNull();
    getResponses = [{ body: { content: 'a: 1\n', hash: 'h1' } }];
    await click('Retry');
    await waitFor(() => expect(textbox().value).toBe('a: 1\n'));
  });

  it('shows a loading status until the file arrives', async () => {
    render(<ComposeEditor stackName="web" />);
    expect(screen.getByRole('status').textContent).toBe('Loading compose file…');
    await screen.findByRole('textbox', { name: /compose file/i });
    expect(screen.queryByText(/Loading compose file/)).toBeNull();
  });

  it('a failed GET after Discard shows the error with Retry', async () => {
    putResponses = [{ status: 409, body: { error: 'changed' } }];
    const { ta } = await loaded();
    edit(ta, 'mine\n');
    await click('Save');
    getResponses = [{ status: 500, body: { error: 'reload broke' } }];
    await userEvent.click(await screen.findByRole('button', { name: 'Discard my changes' }));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Retry' })).toBeTruthy());
    expect(screen.queryByText(/Loading compose file/)).toBeNull();
  });

  it('typing during a save is kept and the editor stays dirty afterwards', async () => {
    let release!: () => void;
    fake.restore();
    fake = installFakeFetch({
      'GET /api/stacks/web/compose': { body: { content: 'a: 1\n', hash: 'h1' } },
      'PUT /api/stacks/web/compose': () =>
        new Promise<FakeResponse>((r) => {
          release = () => r({ body: { hash: 'h2' } });
        }),
    });
    const { ta } = await loaded();
    edit(ta, 'sent\n');
    await click('Save');
    await waitFor(() => expect(release).toBeTypeOf('function'));
    const live = textbox();
    expect(live.readOnly).toBe(false);
    edit(live, 'sent\nmore\n');
    release();
    await waitFor(() => expect(puts()).toHaveLength(1));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Save' }).getAttribute('aria-disabled')).not.toBe('true'));
    expect(textbox().value).toBe('sent\nmore\n');
  });

  it('exposes dirty to the parent via onDirtyChange', async () => {
    const seen: boolean[] = [];
    const { ta } = await loaded({ onDirtyChange: (d) => seen.push(d) });
    edit(ta, 'x\n');
    await waitFor(() => expect(seen.at(-1)).toBe(true));
    await click('Save');
    await waitFor(() => expect(seen.at(-1)).toBe(false));
  });

  it('names the backup after composePath, with a generic fallback', async () => {
    const { ta } = await loaded({ composePath: '/srv/web/docker-compose.yml' });
    edit(ta, 'x\n');
    await click('Save');
    await waitFor(() =>
      expect(get(toasts).some((t) => t.message === 'Saved — previous version kept as docker-compose.yml.bak')).toBe(true),
    );
  });

  it('falls back to a generic backup message without composePath', async () => {
    const { ta } = await loaded();
    edit(ta, 'x\n');
    await click('Save');
    await waitFor(() => expect(get(toasts).some((t) => t.message.includes('.bak file next to it'))).toBe(true));
  });

  it('soft-disables Preview changes during a conflict', async () => {
    putResponses = [{ status: 409, body: { error: 'changed' } }];
    const { ta } = await loaded();
    edit(ta, 'mine\n');
    await click('Save');
    await screen.findByRole('button', { name: 'Reload' });
    expect(screen.getByRole('button', { name: 'Preview changes' }).getAttribute('aria-disabled')).toBe('true');
  });

  it('soft-disables Save and ignores Ctrl+S during a conflict, naming the way out', async () => {
    putResponses = [{ status: 409, body: { error: 'changed' } }];
    const { ta } = await loaded();
    edit(ta, 'mine\n');
    await click('Save');
    await screen.findByRole('button', { name: 'Reload' });
    const save = screen.getByRole('button', { name: 'Save' });
    expect(save.getAttribute('aria-disabled')).toBe('true');
    expect(save.getAttribute('title') ?? save.textContent).toContain('Resolve the conflict first');
    const before = puts().length;
    fireEvent.keyDown(ta, { key: 's', ctrlKey: true });
    await userEvent.click(save);
    await new Promise((r) => setTimeout(r, 30));
    expect(puts()).toHaveLength(before);
  });

  it('beforeunload is cancelled while dirty (only defaultPrevented is asserted; jsdom coerces returnValue)', async () => {
    const { ta } = await loaded();
    edit(ta, 'changed\n');
    await waitFor(() => {
      const e = new Event('beforeunload', { cancelable: true }) as BeforeUnloadEvent;
      window.dispatchEvent(e);
      expect(e.defaultPrevented).toBe(true);
    });
  });

  it('loads and shows the file; Save is disabled until the draft changes', async () => {
    await loaded();
    const save = screen.getByRole('button', { name: 'Save' });
    expect(save.getAttribute('aria-disabled')).toBe('true');
    expect(save.getAttribute('title')).toBe('No changes to save');
  });

  it('saves {content, hash}, toasts, and only then offers Apply', async () => {
    const onSaved = vi.fn();
    const onApply = vi.fn();
    const { ta } = await loaded({ onSaved, onApply });
    expect(screen.queryByRole('button', { name: 'Apply' })).toBeNull();
    edit(ta, 'a: 2\n');
    await click('Save');
    await waitFor(() => expect(onSaved).toHaveBeenCalledWith('h2'));
    expect(puts()[0].body).toEqual({ content: 'a: 2\n', hash: 'h1' });
    expect(get(toasts).some((t) => t.kind === 'success' && t.message.includes('.bak'))).toBe(true);
    await userEvent.click(await screen.findByRole('button', { name: 'Apply' }));
    expect(onApply).toHaveBeenCalledOnce();
  });

  it('warns when port and IP checks were partial', async () => {
    putResponses = [{ body: { hash: 'h2' }, headers: { 'X-Stackstead-Checks': 'partial' } }];
    const { ta } = await loaded();
    edit(ta, 'a: 2\n');
    await click('Save');
    await waitFor(() =>
      expect(get(toasts).some((t) => t.kind === 'warning' && t.message === 'Port and IP checks were partial (Docker unreachable)')).toBe(true),
    );
  });

  it('Preview changes shows the diff and a Save button; Ctrl+S saves', async () => {
    const { ta } = await loaded();
    edit(ta, 'a: 2\n');
    await click('Preview changes');
    const diff = screen.getByRole('group', { name: 'Changes to compose.yaml' });
    expect(diff.textContent).toContain('a: 2');
    expect(screen.getByText('Showing the file as it is now against your draft.')).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Back to editing' })).toBeTruthy();
    fireEvent.keyDown(diff, { key: 's', ctrlKey: true });
    await waitFor(() => expect(puts()).toHaveLength(1));
  });

  it('TestConflictOffersReloadAndKeepsDraft', async () => {
    putResponses = [{ status: 409, body: { error: 'compose file changed on disk' } }];
    const { ta } = await loaded();
    edit(ta, 'mine: 1\n');
    await click('Save');
    expect((await screen.findByRole('alert')).textContent).toContain('compose file changed on disk');
    expect(textbox().value).toBe('mine: 1\n'); // draft untouched

    getResponses = [{ body: { content: 'a: 9\n', hash: 'h9' } }];
    await click('Reload');
    // New content vs the draft is shown as a diff, and the draft survives.
    const diff = await screen.findByRole('group', { name: /changes/i });
    await waitFor(() => expect(diff.textContent).toContain('mine: 1'));
    expect(screen.queryByText('compose file changed on disk')).toBeNull();
    // The retry uses the new hash.
    await click('Save');
    await waitFor(() => expect(puts()).toHaveLength(2));
    expect(puts()[1].body).toEqual({ content: 'mine: 1\n', hash: 'h9' });
  });

  it('Discard my changes reloads and drops the draft', async () => {
    putResponses = [{ status: 409, body: { error: 'changed' } }];
    const { ta } = await loaded();
    edit(ta, 'mine\n');
    await click('Save');
    getResponses = [{ body: { content: 'a: 9\n', hash: 'h9' } }];
    await userEvent.click(await screen.findByRole('button', { name: 'Discard my changes' }));
    await screen.findByRole('textbox', { name: /compose file/i });
    await waitFor(() => expect(textbox().value).toBe('a: 9\n'));
  });

  it('shows a 422 verbatim as text in a <pre>, keeps the draft, and moves the cursor to the line', async () => {
    const msg = 'yaml: line 2: bad <b>indent</b>';
    putResponses = [{ status: 422, body: { error: msg } }];
    const { ta } = await loaded();
    edit(ta, 'a: 1\nb:\n  - x\n');
    await click('Save');
    const pre = await screen.findByText(msg);
    expect(pre.tagName).toBe('PRE');
    expect(pre.getAttribute('data-testid')).toBe('compose-error');
    expect(pre.querySelector('b')).toBeNull();
    expect(textbox().value).toBe('a: 1\nb:\n  - x\n');
    await waitFor(() => expect(textbox().selectionStart).toBe('a: 1\n'.length));
  });

  it('shows the parseError banner for raw-only stacks', async () => {
    await loaded({ parseError: 'line 4: did not find expected key' });
    const banner = screen.getByRole('alert');
    expect(banner.textContent).toContain('This file cannot be edited with forms');
    expect(banner.querySelector('pre')?.textContent).toBe('line 4: did not find expected key');
  });

  it('guards beforeunload only while the draft differs from the file', async () => {
    const { ta } = await loaded();
    const ev = () => {
      const e = new Event('beforeunload', { cancelable: true });
      window.dispatchEvent(e);
      return e.defaultPrevented;
    };
    expect(ev()).toBe(false);
    edit(ta, 'changed\n');
    await waitFor(() => expect(ev()).toBe(true));
  });
});
