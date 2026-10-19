import { get } from '$lib/store';
import { describe, expect, it, vi } from 'vitest';
import { ApiError } from '../api/client';
import { createComposeEditor } from './compose';

type Raw = { content: string; hash: string };
type Saved = { hash: string; checksPartial?: boolean };

function setup(o: { load?: () => Promise<Raw>; save?: (c: string, h: string) => Promise<Saved> } = {}) {
  const load = vi.fn(o.load ?? (async () => ({ content: 'a: 1\n', hash: 'h1' })));
  const save = vi.fn(o.save ?? (async () => ({ hash: 'h2' })));
  const ed = createComposeEditor('web', { load, save });
  return { ed, load, save, s: () => get(ed.state) };
}
const conflict = async (): Promise<Saved> => {
  throw new ApiError(409, 'compose file changed on disk');
};

describe('compose state machine', () => {
  it('starts loading, then edits with draft equal to content', async () => {
    const { ed, s } = setup();
    expect(s().phase).toBe('loading');
    await ed.reload();
    expect(s()).toMatchObject({ phase: 'editing', content: 'a: 1\n', draft: 'a: 1\n', hash: 'h1' });
  });

  it('edit -> preview -> save sends the draft with the loaded hash', async () => {
    const { ed, save, s } = setup();
    await ed.reload();
    ed.setDraft('a: 2\n');
    ed.preview();
    expect(s().phase).toBe('previewing');
    expect(await ed.save()).toBe(true);
    expect(save).toHaveBeenCalledWith('a: 2\n', 'h1');
    expect(s()).toMatchObject({ phase: 'saved', content: 'a: 2\n', draft: 'a: 2\n', hash: 'h2' });
  });

  it('cancelPreview returns to editing and keeps the draft', async () => {
    const { ed, s } = setup();
    await ed.reload();
    ed.setDraft('x');
    ed.preview();
    ed.cancelPreview();
    expect(s()).toMatchObject({ phase: 'editing', draft: 'x' });
  });

  it('editing after a save goes back to editing', async () => {
    const { ed, s } = setup();
    await ed.reload();
    ed.setDraft('b');
    await ed.save();
    ed.setDraft('c');
    expect(s().phase).toBe('editing');
  });

  it('409 enters conflict with the server message verbatim, keeping draft and old hash', async () => {
    const { ed, s } = setup({ save: conflict });
    await ed.reload();
    ed.setDraft('mine');
    expect(await ed.save()).toBe(false);
    expect(s()).toMatchObject({ phase: 'conflict', conflictMessage: 'compose file changed on disk', draft: 'mine', hash: 'h1' });
  });

  it('reload keepDraft takes new content+hash, keeps the draft, previews new -> draft', async () => {
    let n = 0;
    const { ed, s, save } = setup({
      load: async () => (n++ === 0 ? { content: 'a: 1\n', hash: 'h1' } : { content: 'a: 9\n', hash: 'h9' }),
      save: conflict,
    });
    await ed.reload();
    ed.setDraft('mine');
    await ed.save();
    await ed.reload({ keepDraft: true });
    expect(s()).toMatchObject({ phase: 'previewing', content: 'a: 9\n', hash: 'h9', draft: 'mine' });
    expect(s().conflictMessage).toBeUndefined();
    await ed.save();
    expect(save).toHaveBeenLastCalledWith('mine', 'h9');
  });

  it('reload without keepDraft discards the draft', async () => {
    const { ed, s } = setup({ save: conflict });
    await ed.reload();
    ed.setDraft('mine');
    await ed.save();
    await ed.reload({ keepDraft: false });
    expect(s()).toMatchObject({ phase: 'editing', draft: 'a: 1\n' });
  });

  it('422 keeps the draft and the compose message verbatim, stays editable', async () => {
    const msg = 'yaml: line 3: mapping values are not allowed\n<b>x</b>';
    const { ed, s } = setup({ save: async () => { throw new ApiError(422, msg); } });
    await ed.reload();
    ed.setDraft('bad');
    expect(await ed.save()).toBe(false);
    expect(s()).toMatchObject({ phase: 'editing', draft: 'bad', error: msg, hash: 'h1' });
  });

  it('a network error keeps the draft and reports the message', async () => {
    const { ed, s } = setup({ save: async () => { throw new TypeError('Failed to fetch'); } });
    await ed.reload();
    ed.setDraft('d');
    expect(await ed.save()).toBe(false);
    expect(s()).toMatchObject({ phase: 'editing', draft: 'd', error: 'Failed to fetch' });
  });

  it('a failed initial load leaves an error and no editable content', async () => {
    const { ed, s } = setup({ load: async () => { throw new ApiError(500, 'boom'); } });
    await ed.reload();
    expect(s().error).toBe('boom');
    expect(s().phase).toBe('loadFailed');
  });

  it('ignores a second save while one is in flight', async () => {
    let release!: () => void;
    const { ed, save } = setup({ save: () => new Promise<Saved>((r) => { release = () => r({ hash: 'h2' }); }) });
    await ed.reload();
    ed.setDraft('x');
    const first = ed.save();
    expect(await ed.save()).toBe(false);
    release();
    expect(await first).toBe(true);
    expect(save).toHaveBeenCalledTimes(1);
  });

  it('exposes checksPartial from the save result', async () => {
    const { ed, s } = setup({ save: async () => ({ hash: 'h2', checksPartial: true }) });
    await ed.reload();
    ed.setDraft('x');
    await ed.save();
    expect(s().checksPartial).toBe(true);
  });

  it('keeps edits made while a save is in flight: still dirty after the save', async () => {
    let release!: () => void;
    const { ed, s } = setup({ save: () => new Promise<Saved>((r) => { release = () => r({ hash: 'h2' }); }) });
    await ed.reload();
    ed.setDraft('sent');
    const p = ed.save();
    ed.setDraft('typed meanwhile');
    release();
    await p;
    expect(s()).toMatchObject({ content: 'sent', draft: 'typed meanwhile', hash: 'h2', phase: 'editing' });
  });
});
