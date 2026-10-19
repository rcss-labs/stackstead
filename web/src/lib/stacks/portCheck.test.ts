import { describe, expect, it } from 'vitest';
import type { ContainerRow } from '../api/types';
import { hostPorts, portVerdict, publishedPorts, type PublishedPort } from './portCheck';

function row(name: string, over: Partial<ContainerRow> = {}): ContainerRow {
  return {
    id: name.padEnd(12, '0').slice(0, 12),
    name,
    image: 'nginx:1',
    imageId: '',
    state: 'running',
    status: 'Up 1 hour',
    health: 'none',
    exitCode: null,
    createdAt: '2026-01-01T00:00:00Z',
    project: '',
    service: '',
    stack: '',
    managed: 'none',
    ports: [],
    networks: {},
    cpu: null,
    mem: null,
    memLimit: null,
    restarts24h: 0,
    ...over,
  };
}

describe('hostPorts', () => {
  it.each([
    ['8080:80', { lo: 8080, hi: 8080, proto: 'tcp' }],
    ['127.0.0.1:8080:80', { lo: 8080, hi: 8080, proto: 'tcp' }],
    ['[::1]:8080:80', { lo: 8080, hi: 8080, proto: 'tcp' }],
    ['8000-8010:80-90/udp', { lo: 8000, hi: 8010, proto: 'udp' }],
    [' 8080:80 ', { lo: 8080, hi: 8080, proto: 'tcp' }],
    ['80', undefined],
    ['80/udp', undefined],
    ['${PORT}:80', undefined],
    ['8080:80/quic', undefined],
    ['70000:80', undefined],
    ['8010-8000:80', undefined],
    ['', undefined],
  ])('parses %j', (spec, expected) => {
    expect(hostPorts(spec)).toEqual(expected);
  });
});

describe('publishedPorts', () => {
  it('parses host port, protocol and the owner (stack before name)', () => {
    const rows = [
      row('ha', { stack: 'home-assistant', ports: ['0.0.0.0:8123->8123/tcp', '[::]:1883->1883/tcp'] }),
      row('loose', { ports: ['127.0.0.1:53->53/udp'] }),
    ];
    expect(publishedPorts(rows)).toEqual([
      { port: 8123, proto: 'tcp', who: 'home-assistant' },
      { port: 1883, proto: 'tcp', who: 'home-assistant' },
      { port: 53, proto: 'udp', who: 'loose' },
    ]);
  });

  it('skips entries it cannot parse', () => {
    expect(publishedPorts([row('x', { ports: ['garbage', '80/tcp'] })])).toEqual([]);
  });
});

describe('portVerdict', () => {
  const own = { service: 'web', index: 0 };
  const tcp = (port: number, who: string): PublishedPort => ({ port, proto: 'tcp', who });

  it('reports a published port as used', () => {
    expect(portVerdict('8123:8123', own, [], [tcp(8123, 'home-assistant')])).toEqual({
      tone: 'danger',
      text: 'Port 8123 is used by home-assistant',
    });
  });

  it('is protocol-aware', () => {
    expect(portVerdict('53:53/udp', own, [], [tcp(53, 'dns')])).toEqual({ tone: 'success', text: 'Port 53 is free' });
  });

  it('ignores the host ip', () => {
    expect(portVerdict('127.0.0.1:8123:80', own, [], [tcp(8123, 'x')])?.tone).toBe('danger');
  });

  it('reports the first conflicting port of a range', () => {
    expect(portVerdict('8000-8010:80-90', own, [], [tcp(8005, 'y')])).toEqual({ tone: 'danger', text: 'Port 8005 is used by y' });
  });

  it('says a free range is free', () => {
    expect(portVerdict('8000-8010:80-90', own, [], [])).toEqual({ tone: 'success', text: 'Ports 8000-8010 are free' });
  });

  it('flags a port another card published earlier in the form', () => {
    const formPorts = [
      { service: 'web', index: 0, spec: '8080:80' },
      { service: 'db', index: 0, spec: '8080:5432' },
    ];
    expect(portVerdict('8080:5432', { service: 'db', index: 0 }, formPorts, [])).toEqual({
      tone: 'warning',
      text: 'Port 8080 is also published by web in this stack',
    });
  });

  it('does not flag the same row against itself or against later rows', () => {
    const formPorts = [
      { service: 'web', index: 0, spec: '8080:80' },
      { service: 'db', index: 0, spec: '8080:80' },
    ];
    expect(portVerdict('8080:80', own, formPorts, [])?.tone).toBe('success');
  });

  it('names an unnamed card "another service"', () => {
    const formPorts = [
      { service: '', index: 0, spec: '8080:80' },
      { service: 'db', index: 0, spec: '8080:80' },
    ];
    expect(portVerdict('8080:80', { service: 'db', index: 0 }, formPorts, [])?.text).toBe(
      'Port 8080 is also published by another service in this stack',
    );
  });

  it('prefers the container conflict over the in-form one', () => {
    const formPorts = [
      { service: 'web', index: 0, spec: '8080:80' },
      { service: 'db', index: 0, spec: '8080:80' },
    ];
    expect(portVerdict('8080:80', { service: 'db', index: 0 }, formPorts, [tcp(8080, 'proxy')])?.tone).toBe('danger');
  });

  it('is silent for container-only, templated and malformed specs', () => {
    for (const spec of ['80', '${P}:80', 'abc']) expect(portVerdict(spec, own, [], [])).toBeUndefined();
  });

  it('is silent while the containers list is unknown', () => {
    expect(portVerdict('8080:80', own, [], undefined)).toBeUndefined();
  });
});
