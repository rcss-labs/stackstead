import { describe, expect, it } from 'vitest';
import type { StackDetail } from '$lib/api/types';
import { detailHref, itemHref, missingComposeFile, resolveDetailLocation } from './detailLocation';

const detail = (names: string[], parseError?: string): Pick<StackDetail, 'services' | 'parseError'> => ({
  services: names.map((name) => ({ name, config: { limits: {} }, readOnly: [], containers: [] })),
  parseError,
});
const q = (s: string) => new URLSearchParams(s);
const two = detail(['app', 'db']);
const built = { ...two, services: two.services.map((s) => ({ ...s, config: { build: { context: '.' }, limits: {} } })) };

describe('resolveDetailLocation', () => {
  it('defaults to the first service overview', () => {
    const loc = resolveDetailLocation(q(''), two);
    expect(loc.item).toEqual({ kind: 'service', name: 'app' });
    expect(loc.serviceTab).toBe('overview');
    expect(loc.logsRequested).toBe(false);
    expect(loc.logService).toBeUndefined();
    expect(loc.op).toBeUndefined();
  });

  it('?service= selects a known service and ignores an unknown one', () => {
    expect(resolveDetailLocation(q('service=db'), two).item).toEqual({ kind: 'service', name: 'db' });
    expect(resolveDetailLocation(q('service=nope'), two).item).toEqual({ kind: 'service', name: 'app' });
  });

  it('?tab=metrics and ?tab=config pick the service tab', () => {
    const metrics = resolveDetailLocation(q('service=db&tab=metrics'), two);
    expect(metrics.item).toEqual({ kind: 'service', name: 'db' });
    expect(metrics.serviceTab).toBe('metrics');
    expect(resolveDetailLocation(q('tab=config'), two).serviceTab).toBe('config');
  });

  it('?tab=terminal picks the terminal tab and round-trips in the href', () => {
    const loc = resolveDetailLocation(q('service=db&tab=terminal'), two);
    expect(loc.serviceTab).toBe('terminal');
    expect(itemHref('web', loc.item, loc.serviceTab)).toBe('/stacks/web?service=db&tab=terminal');
  });

  it('detailLocation round-trips tab=dockerfile', () => {
    const loc = resolveDetailLocation(q('service=db&tab=dockerfile'), built);
    expect(loc.item).toEqual({ kind: 'service', name: 'db' });
    expect(loc.serviceTab).toBe('dockerfile');
    expect(itemHref('web', loc.item, loc.serviceTab)).toBe('/stacks/web?service=db&tab=dockerfile');
  });

  it('?tab=dockerfile on a service without a build falls back to overview', () => {
    expect(resolveDetailLocation(q('service=db&tab=dockerfile'), two).serviceTab).toBe('overview');
  });

  it('?tab=compose and ?tab=operations&op=x select stack items', () => {
    expect(resolveDetailLocation(q('tab=compose'), two).item).toEqual({ kind: 'compose' });
    const ops = resolveDetailLocation(q('tab=operations&op=x'), two);
    expect(ops.item).toEqual({ kind: 'operations' });
    expect(ops.op).toBe('x');
  });

  it('?tab=logs requests the console with the service chip from ?service', () => {
    const loc = resolveDetailLocation(q('tab=logs&service=db'), two);
    expect(loc.logsRequested).toBe(true);
    expect(loc.logService).toBe('db');
    expect(loc.item).toEqual({ kind: 'service', name: 'db' });
    expect(loc.serviceTab).toBe('overview');
    const unknown = resolveDetailLocation(q('tab=logs&service=nope'), two);
    expect(unknown.logService).toBeUndefined();
    expect(unknown.logsRequested).toBe(true);
  });

  it('legacy ?tab=services is the overview', () => {
    const loc = resolveDetailLocation(q('tab=services&service=db'), two);
    expect(loc.item).toEqual({ kind: 'service', name: 'db' });
    expect(loc.serviceTab).toBe('overview');
    expect(loc.logsRequested).toBe(false);
  });

  it('a parse error forces compose, a missing file forces operations', () => {
    const broken = detail(['app'], 'yaml: line 3: mapping values are not allowed');
    expect(resolveDetailLocation(q('service=app'), broken).item).toEqual({ kind: 'compose' });
    const missing = detail(['app'], 'open /srv/web/compose.yaml: no such file or directory');
    expect(resolveDetailLocation(q('service=app'), missing).item).toEqual({ kind: 'operations' });
  });

  it('under a parse error ?tab=operations keeps the operations item and its op', () => {
    const broken = detail(['app'], 'yaml: bad indent');
    const loc = resolveDetailLocation(q('tab=operations&op=x'), broken);
    expect(loc.item).toEqual({ kind: 'operations' });
    expect(loc.op).toBe('x');
    expect(resolveDetailLocation(q(''), broken).item).toEqual({ kind: 'compose' });
    expect(resolveDetailLocation(q('tab=compose'), broken).item).toEqual({ kind: 'compose' });
    for (const tab of ['services', 'metrics', 'config', 'bogus']) {
      expect(resolveDetailLocation(q(`tab=${tab}`), broken).item).toEqual({ kind: 'compose' });
    }
  });

  it('under a parse error the console is not requested', () => {
    const loc = resolveDetailLocation(q('tab=logs&service=app'), detail(['app'], 'yaml: bad indent'));
    expect(loc.item).toEqual({ kind: 'compose' });
    expect(loc.logsRequested).toBe(false);
  });

  it('op is only returned for the operations item', () => {
    expect(resolveDetailLocation(q('tab=services&op=x'), two).op).toBeUndefined();
    expect(resolveDetailLocation(q('tab=compose&op=x'), two).op).toBeUndefined();
  });

  it('no services and no parse error gives none', () => {
    expect(resolveDetailLocation(q(''), detail([])).item).toEqual({ kind: 'none' });
  });

  it('undefined detail gives none without logs', () => {
    const loc = resolveDetailLocation(q('tab=logs&service=db'), undefined);
    expect(loc).toEqual({ item: { kind: 'none' }, serviceTab: 'overview', logsRequested: false, logService: undefined, op: undefined });
  });
});

describe('missingComposeFile', () => {
  it('recognises missing-file errors only', () => {
    expect(missingComposeFile('Cannot find compose.yaml')).toBe(true);
    expect(missingComposeFile('yaml: bad indent')).toBe(false);
    expect(missingComposeFile(undefined)).toBe(false);
  });
});

describe('hrefs', () => {
  it('detailHref encodes the name and omits empty params', () => {
    expect(detailHref('my stack', { tab: 'logs', service: 'a#b' })).toBe('/stacks/my%20stack?tab=logs&service=a%23b');
    expect(detailHref('web', {})).toBe('/stacks/web');
    expect(detailHref('web', { tab: '', service: undefined })).toBe('/stacks/web');
  });

  it('itemHref maps items to queries', () => {
    expect(itemHref('web', { kind: 'service', name: 'app' })).toBe('/stacks/web?service=app');
    expect(itemHref('web', { kind: 'service', name: 'app' }, 'config')).toBe('/stacks/web?service=app&tab=config');
    expect(itemHref('web', { kind: 'compose' })).toBe('/stacks/web?tab=compose');
    expect(itemHref('web', { kind: 'operations' })).toBe('/stacks/web?tab=operations');
    expect(itemHref('web', { kind: 'none' })).toBe('/stacks/web');
  });
});
