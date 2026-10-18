import { describe as suite, expect, it } from 'vitest';
import type { UpdateStatus } from '../api/types';
import { describe, errorSentence, safeNotesURL } from './updateState';

const NOW = new Date('2026-10-18T12:00:00Z');
const st = (over: Partial<UpdateStatus> = {}): UpdateStatus => ({
  current: '0.2.0',
  installer: 'deb',
  command: 'sudo /usr/bin/stackstead upgrade',
  state: 'up-to-date',
  latest: '0.2.0',
  notesURL: '',
  checkedAt: '2026-10-18T09:00:00Z',
  error: '',
  ...over,
});

suite('safeNotesURL', () => {
  it('accepts https on github.com', () => {
    expect(safeNotesURL('https://github.com/rcss-labs/stackstead/releases/tag/v0.3.0')).toBe(
      'https://github.com/rcss-labs/stackstead/releases/tag/v0.3.0',
    );
  });
  it('rejects http, other hosts, userinfo, lookalikes and junk', () => {
    expect(safeNotesURL('http://github.com/x')).toBeUndefined();
    expect(safeNotesURL('https://example.com/x')).toBeUndefined();
    expect(safeNotesURL('https://user:pw@github.com/x')).toBeUndefined();
    expect(safeNotesURL('https://github.com.evil.example/x')).toBeUndefined();
    expect(safeNotesURL('javascript:alert(1)')).toBeUndefined();
    expect(safeNotesURL('')).toBeUndefined();
  });
});

suite('errorSentence', () => {
  it('has a sentence per code', () => {
    expect(errorSentence('network')).toBe('no route to github.com');
    expect(errorSentence('timeout')).toBe('the release source did not answer in time');
    expect(errorSentence('http')).toBe('the release source answered with an error');
    expect(errorSentence('malformed')).toBe('the release listing could not be read');
  });
  it('never shows an unknown slug', () => {
    const s = errorSentence('weird-slug');
    expect(s).not.toContain('weird');
    expect(s.length).toBeGreaterThan(0);
  });
});

suite('describe', () => {
  it('up-to-date', () => {
    const d = describe(st(), NOW);
    expect(d.pill).toBe('up to date');
    expect(d.tone).toBe('success');
    expect(d.headline).toBe('Stackstead v0.2.0 is the latest release.');
    expect(d.detail).toBe('Checked 3h ago.');
  });
  it('up-to-date with a later failure keeps the result and adds the sentence', () => {
    const d = describe(st({ error: 'timeout' }), NOW);
    expect(d.pill).toBe('up to date');
    expect(d.detail).toBe(
      'The last check failed (the release source did not answer in time); showing the result from 3h ago.',
    );
  });
  it('available', () => {
    const d = describe(st({ state: 'available', latest: '0.3.0' }), NOW);
    expect(d.pill).toBe('update available');
    expect(d.tone).toBe('accent');
    expect(d.headline).toBe('v0.2.0 → v0.3.0');
    expect(d.detail).toBe('Checked 3h ago.');
  });
  it('available with a later failure adds the sentence', () => {
    const d = describe(st({ state: 'available', latest: '0.3.0', error: 'http' }), NOW);
    expect(d.pill).toBe('update available');
    expect(d.detail).toContain('the release source answered with an error');
    expect(d.detail).toContain('3h ago');
  });
  it('failed with no earlier success', () => {
    const d = describe(st({ state: 'failed', latest: '', checkedAt: undefined, error: 'network' }), NOW);
    expect(d.pill).toBe('check failed');
    expect(d.tone).toBe('danger');
    expect(d.headline).toBe(
      'Could not reach the release source (no route to github.com). Nothing is known about newer releases.',
    );
    expect(d.detail).toBe('You can still upgrade from the terminal.');
  });
  it('failed with an unknown slug does not leak it', () => {
    const d = describe(st({ state: 'failed', checkedAt: undefined, error: 'weird-slug' }), NOW);
    expect(d.headline).not.toContain('weird-slug');
  });
  it('disabled', () => {
    const d = describe(st({ state: 'disabled', checkedAt: undefined, latest: '' }), NOW);
    expect(d.pill).toBe('checks off');
    expect(d.tone).toBe('muted');
    expect(d.headline).toBe('Update checks are disabled (update_check: false).');
  });
  it('unknown on a dev build', () => {
    const d = describe(st({ state: 'unknown', current: 'dev-dirty', checkedAt: undefined, latest: '' }), NOW);
    expect(d.pill).toBe('dev build');
    expect(d.tone).toBe('muted');
    expect(d.headline).toBe('This is a development build (dev-dirty); versions cannot be compared.');
  });
  it('unknown on a release build before the first check is "Not checked yet"', () => {
    const d = describe(st({ state: 'unknown', checkedAt: undefined, latest: '' }), NOW);
    expect(d.pill).toBe('not checked');
    expect(d.headline).toBe('Not checked yet.');
  });
});
