import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { get } from 'svelte/store';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { toasts } from '../stores/toasts';
import { installFakeFetch, type FakeFetch, type FakeResponse, type RecordedCall } from '../../vitest-setup';

// Keep the plain textarea: the CodeMirror chunk is exercised in cm.test.ts / DiffView.test.ts.
vi.mock('./cm', () => ({
  createEditor: () => {
    throw new Error('chunk unavailable in this test');
  },
  createDiffView: () => {
    throw new Error('chunk unavailable in this test');
  },
}));

import ComposeEditor from './ComposeEditor.svelte';

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
afterEach(() => fake.restore());

const puts = (): RecordedCall[] => fake.calls.filter((c) => c.method === 'PUT');
async function loaded(props: Record<string, unknown> = {}) {
  const r = render(ComposeEditor, { stackName: 'web', ...props });
  const ta = (await screen.findByRole('textbox', { name: /compose file/i })) as HTMLTextAreaElement;
  await waitFor(() => expect(ta.value).toBe('a: 1\n'));
  return { ...r, ta };
}
const edit = (ta: HTMLTextAreaElement, v: string) => fireEvent.input(ta, { target: { value: v } });

describe('ComposeEditor', () => {
  it('loads and shows the file; Save is disabled until the draft changes', async () => {
    await loaded();
    expect(screen.getByRole('button', { name: 'Save' }).getAttribute('aria-disabled')).toBe('true');
  });

  it('saves {content, hash}, toasts, and only then offers Apply', async () => {
    const onsaved = vi.fn();
    const onapply = vi.fn();
    const { ta } = await loaded({ onsaved, onapply });
    expect(screen.queryByRole('button', { name: 'Apply' })).toBeNull();
    await edit(ta, 'a: 2\n');
    await userEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(onsaved).toHaveBeenCalledWith('h2'));
    expect(puts()[0].body).toEqual({ content: 'a: 2\n', hash: 'h1' });
    expect(get(toasts).some((t) => t.kind === 'success' && t.message.includes('compose.yaml.bak'))).toBe(true);
    await userEvent.click(await screen.findByRole('button', { name: 'Apply' }));
    expect(onapply).toHaveBeenCalledOnce();
  });

  it('warns when port and IP checks were partial', async () => {
    putResponses = [{ body: { hash: 'h2' }, headers: { 'X-Stackstead-Checks': 'partial' } }];
    const { ta } = await loaded();
    await edit(ta, 'a: 2\n');
    await userEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(get(toasts).some((t) => t.kind === 'warning' && /partial/.test(t.message))).toBe(true));
  });

  it('Preview changes shows the diff and a Save button; Ctrl+S saves', async () => {
    const { ta } = await loaded();
    await edit(ta, 'a: 2\n');
    await userEvent.click(screen.getByRole('button', { name: 'Preview changes' }));
    expect(screen.getByRole('group', { name: /changes/i }).textContent).toContain('a: 2');
    await fireEvent.keyDown(screen.getByRole('group', { name: /changes/i }), { key: 's', ctrlKey: true });
    await waitFor(() => expect(puts()).toHaveLength(1));
  });

  it('TestConflictOffersReloadAndKeepsDraft', async () => {
    putResponses = [{ status: 409, body: { error: 'compose file changed on disk' } }];
    const { ta } = await loaded();
    await edit(ta, 'mine: 1\n');
    await userEvent.click(screen.getByRole('button', { name: 'Save' }));
    expect((await screen.findByRole('alert')).textContent).toContain('compose file changed on disk');
    expect(ta.value).toBe('mine: 1\n'); // draft untouched

    getResponses = [{ body: { content: 'a: 9\n', hash: 'h9' } }];
    await userEvent.click(screen.getByRole('button', { name: 'Reload' }));
    // New content vs the draft is shown as a diff, and the draft survives.
    const diff = await screen.findByRole('group', { name: /changes/i });
    await waitFor(() => expect(diff.textContent).toContain('mine: 1'));
    expect(screen.queryByText('compose file changed on disk')).toBeNull();
    // The retry uses the new hash.
    await userEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(puts()).toHaveLength(2));
    expect(puts()[1].body).toEqual({ content: 'mine: 1\n', hash: 'h9' });
  });

  it('Discard my changes reloads and drops the draft', async () => {
    putResponses = [{ status: 409, body: { error: 'changed' } }];
    const { ta } = await loaded();
    await edit(ta, 'mine\n');
    await userEvent.click(screen.getByRole('button', { name: 'Save' }));
    getResponses = [{ body: { content: 'a: 9\n', hash: 'h9' } }];
    await userEvent.click(await screen.findByRole('button', { name: 'Discard my changes' }));
    const ta2 = (await screen.findByRole('textbox', { name: /compose file/i })) as HTMLTextAreaElement;
    await waitFor(() => expect(ta2.value).toBe('a: 9\n'));
  });

  it('shows a 422 verbatim as text in a <pre>, keeps the draft, and moves the cursor to the line', async () => {
    const msg = 'yaml: line 2: bad <b>indent</b>';
    putResponses = [{ status: 422, body: { error: msg } }];
    const { ta } = await loaded();
    await edit(ta, 'a: 1\nb:\n  - x\n');
    await userEvent.click(screen.getByRole('button', { name: 'Save' }));
    const pre = await screen.findByText(msg);
    expect(pre.tagName).toBe('PRE');
    expect(pre.querySelector('b')).toBeNull();
    expect(ta.value).toBe('a: 1\nb:\n  - x\n');
    await waitFor(() => expect(ta.selectionStart).toBe('a: 1\n'.length));
  });

  it('shows the parseError banner for raw-only stacks', async () => {
    await loaded({ parseError: 'line 4: did not find expected key' });
    expect(screen.getByRole('alert').textContent).toContain('line 4: did not find expected key');
  });

  it('guards beforeunload only while the draft differs from the file', async () => {
    const { ta } = await loaded();
    const ev = () => {
      const e = new Event('beforeunload', { cancelable: true });
      window.dispatchEvent(e);
      return e.defaultPrevented;
    };
    expect(ev()).toBe(false);
    await edit(ta, 'changed\n');
    await waitFor(() => expect(ev()).toBe(true));
  });
});
