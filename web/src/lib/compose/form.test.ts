import { describe, expect, it } from 'vitest';
import type { ServiceConfig } from '../api/types';
import { emptyForm, fieldOf, fromForm, mapServerErrors, toForm, validateForm, type ServiceForm } from './form';

const full: ServiceConfig = {
  build: { context: './app', dockerfile: 'Dockerfile.dev' },
  ports: ['8080:80', '127.0.0.1:9090:90'],
  volumes: ['data:/var/lib/data'],
  environment: [
    { name: 'A', value: '1' },
    { name: 'B', value: '' },
  ],
  networks: [
    { name: 'lan', ipv4Address: '10.0.0.5' },
    { name: 'back' },
  ],
  networkMode: 'host',
  limits: { cpus: '0.5', memory: '512M' },
  restart: 'unless-stopped',
  healthcheck: { test: ['CMD', 'curl', '-f', 'http://localhost'], interval: '30s', timeout: '5s', startPeriod: '10s', retries: 3 },
};

function form(patch: Partial<ServiceForm> = {}): ServiceForm {
  return { ...emptyForm(), image: 'nginx:1', ...patch };
}
const hc = (patch: Partial<ServiceForm['healthcheck']>) => ({ ...emptyForm().healthcheck, enabled: true, test: 'true', ...patch });

describe('toForm / fromForm', () => {
  it('round-trips a full config', () => {
    expect(fromForm(toForm(full, []), full)).toEqual(full);
  });

  it('round-trips an image config and always keeps limits', () => {
    const cfg: ServiceConfig = { image: 'redis:7', limits: {} };
    expect(toForm(cfg, []).source).toBe('image');
    expect(fromForm(toForm(cfg, []), cfg)).toEqual(cfg);
  });

  it('emits only limits for an empty form', () => {
    expect(fromForm(emptyForm(), null)).toEqual({ limits: {} });
  });

  it('omits blank rows and empty values', () => {
    const f = form({
      ports: ['  ', '80'],
      volumes: [''],
      env: [{ name: '', value: '' }, { name: 'X', value: 'y' }],
      networks: [{ name: '', ipv4Address: '' }, { name: 'lan', ipv4Address: '' }],
    });
    expect(fromForm(f, null)).toEqual({
      image: 'nginx:1',
      ports: ['80'],
      environment: [{ name: 'X', value: 'y' }],
      networks: [{ name: 'lan' }],
      limits: {},
    });
  });

  it('uses build and omits an empty dockerfile when source is build', () => {
    const f = form({ source: 'build', buildContext: '.', image: '' });
    expect(fromForm(f, null)).toEqual({ build: { context: '.' }, limits: {} });
  });

  it('keeps the original config out of enumerable keys', () => {
    expect(Object.keys(toForm(full, []))).not.toContain('_orig');
    expect(Object.keys(toForm(full, [])).sort()).toEqual(Object.keys(emptyForm()).sort());
  });

  describe('healthcheck test', () => {
    const cases: [string, string[] | undefined, string][] = [
      ['exec form is shown and split back', ['CMD', 'pg_isready', '-U', 'x'], 'CMD pg_isready -U x'],
      ['shell form is shown bare', ['CMD-SHELL', 'curl -f http://x || exit 1'], 'curl -f http://x || exit 1'],
    ];
    for (const [name, test, shown] of cases) {
      it(name, () => {
        const cfg: ServiceConfig = { image: 'a', limits: {}, healthcheck: { test } };
        const f = toForm(cfg, []);
        expect(f.healthcheck.test).toBe(shown);
        expect(fromForm(f, cfg).healthcheck?.test).toEqual(test);
      });
    }
    it('explicit CMD-SHELL prefix keeps the rest as one string', () => {
      const f = form({ healthcheck: hc({ test: 'CMD-SHELL curl -f  http://x' }) });
      expect(fromForm(f, null).healthcheck?.test).toEqual(['CMD-SHELL', 'curl -f  http://x']);
    });
    it('explicit CMD prefix splits on whitespace', () => {
      const f = form({ healthcheck: hc({ test: 'CMD  a   b' }) });
      expect(fromForm(f, null).healthcheck?.test).toEqual(['CMD', 'a', 'b']);
    });
    it('bare text becomes a single shell string', () => {
      const f = form({ healthcheck: hc({ test: 'curl -f http://x' }) });
      expect(fromForm(f, null).healthcheck?.test).toEqual(['CMD-SHELL', 'curl -f http://x']);
    });
    it('retries 0 is kept, blank retries omitted, disabled healthcheck omitted', () => {
      expect(fromForm(form({ healthcheck: hc({ retries: '0' }) }), null).healthcheck?.retries).toBe(0);
      expect(fromForm(form({ healthcheck: hc({}) }), null).healthcheck).not.toHaveProperty('retries');
      expect(fromForm(form({ healthcheck: { ...hc({}), enabled: false } }), null)).not.toHaveProperty('healthcheck');
    });
  });

  describe('read-only groups', () => {
    it('survive untouched although the form shows them blank', () => {
      // The server leaves read-only groups at their zero value in the config.
      const orig: ServiceConfig = { image: 'app', limits: { memory: '1G' }, ports: ['8080:80'] };
      const g = toForm(orig, ['ports', 'limits']);
      expect(g.readOnly).toEqual(new Set(['ports', 'limits']));
      g.ports = [];
      g.memory = '';
      g.cpus = '';
      expect(fromForm(g, orig)).toEqual({ image: 'app', ports: ['8080:80'], limits: { memory: '1G' } });
    });

    it('ignores edits to a read-only group', () => {
      const g = toForm(full, ['environment', 'healthcheck', 'networks', 'volumes', 'restart', 'network_mode']);
      g.env = [{ name: 'Z', value: 'z' }];
      g.healthcheck.enabled = false;
      g.networks = [];
      g.volumes = ['x:/y'];
      g.restart = 'always';
      expect(fromForm(g, full)).toEqual(full);
    });

    it('keeps image and build from the original when either is read-only', () => {
      const orig: ServiceConfig = { build: { context: '.', dockerfile: 'D' }, limits: {} };
      const g = toForm(orig, ['build']);
      g.source = 'image';
      g.image = 'evil';
      expect(fromForm(g, orig)).toEqual(orig);
    });
  });
});

describe('validateForm', () => {
  const ok = (f: ServiceForm, opts = {}) => expect(validateForm(f, { original: null, ...opts })).toEqual({});
  const bad = (f: ServiceForm, path: string, opts = {}) => expect(validateForm(f, { original: null, ...opts })).toHaveProperty([path]);

  it('accepts a valid form', () => ok(toForm(full, []), { name: 'web', requireName: true, original: full }));

  describe('name', () => {
    for (const n of ['web', 'a', '0app', 'my_app-2', 'a'.repeat(63)]) it(`accepts ${n.slice(0, 10)}`, () => ok(form(), { name: n, requireName: true }));
    for (const n of ['', 'Web', '-a', '_a', 'a b', 'a.b', 'a'.repeat(64)]) it(`rejects "${n.slice(0, 10)}"`, () => bad(form(), 'name', { name: n, requireName: true }));
    it('is not checked unless required or given', () => ok(form(), {}));
    it('is checked when given without requireName', () => bad(form(), 'name', { name: 'Bad' }));
  });

  describe('source', () => {
    it('image required', () => bad(form({ image: ' ' }), 'image'));
    it('build context required', () => bad(form({ source: 'build', buildContext: '' }), 'build.context'));
    it('build does not require an image', () => ok(form({ source: 'build', buildContext: '.', image: '' })));
    it('is skipped when image or build is read-only', () => ok(form({ image: '', readOnly: new Set(['build']) })));
  });

  describe('ports', () => {
    const good = ['[fe80::1%eth0]:80:80', '8080:80', '127.0.0.1:8080:80', '8000-8002:80-82', '53:53/udp', '[::1]:80:80', '80', '${PORT:-80}:80', '$P:80', '127.0.0.1::80', '0:80', '8000-8010:80', '80-82', '::1:80:80', '53:53/SCTP'];
    for (const p of good) it(`accepts ${p}`, () => ok(form({ ports: [p] })));
    const rejected = ['65536:80', '80:0', 'abc', '8000-8002:80-81', '8002-8000:80', '1.2.3.4:80', '[::1:80:80', '999.1.1.1:80:80', '80/foo', '-1:80'];
    for (const p of rejected) it(`rejects "${p}"`, () => bad(form({ ports: ['80', p] }), 'ports[1]'));
    it('rejects too many separators', () => bad(form({ ports: ['80:80:80:80:80'] }), 'ports[0]'));
    it('empty rows are not an error (they are dropped on save)', () => ok(form({ ports: ['', '80'] })));
    it('skipped when ports is read-only', () => ok(form({ ports: ['nonsense'], readOnly: new Set(['ports']) })));
  });

  describe('env', () => {
    for (const n of ['A', '_x', 'A1_b', 'discovery.type', 'a-b', '1A']) it(`accepts ${n}`, () => ok(form({ env: [{ name: n, value: '' }] })));
    for (const n of ['a b', 'A=B', 'a\tb', 'a\u0001']) it(`rejects ${n}`, () => bad(form({ env: [{ name: n, value: '' }] }), 'env[0].name'));
    it('flags the second duplicate only', () => {
      const e = validateForm(form({ env: [{ name: 'A', value: '1' }, { name: 'B', value: '' }, { name: 'A', value: '2' }] }), { original: null });
      expect(Object.keys(e)).toEqual(['env[2].name']);
    });
    it('name required when a value is set', () => bad(form({ env: [{ name: '', value: 'x' }] }), 'env[0].name'));
    it('fully blank row ignored', () => ok(form({ env: [{ name: '', value: '' }] })));
  });

  describe('limits', () => {
    for (const c of ['0.5', '2', '.5', '1.25']) it(`cpus accepts ${c}`, () => ok(form({ cpus: c })));
    for (const c of ['0', '0.0', '-1', 'a', '1.', '1e3', '1,5']) it(`cpus rejects ${c}`, () => bad(form({ cpus: c }), 'limits.cpus'));
    for (const m of ['512', '512M', '1g', '1.5G', '64k', '10MB', '2Gb', '1GiB', '512Mi', '1TB', '512 M', '1pb']) it(`memory accepts ${m}`, () => ok(form({ memory: m })));
    for (const m of ['M', '-5M', 'abc', '1.G', '1  G', '1X']) it(`memory rejects ${m}`, () => bad(form({ memory: m }), 'limits.memory'));
  });

  describe('networks', () => {
    for (const ip of ['10.0.0.5', '255.255.255.255', '0.0.0.0']) it(`accepts ${ip}`, () => ok(form({ networks: [{ name: 'lan', ipv4Address: ip }] })));
    for (const ip of ['10.0.0', '256.0.0.1', '10.0.0.5.1', 'a.b.c.d', '01.0.0.1', '::1', '10.0.0.-1']) it(`rejects ${ip}`, () => bad(form({ networks: [{ name: 'a' }, { name: 'lan', ipv4Address: ip }].map((n) => ({ ipv4Address: '', ...n })) }), 'networks[1].ipv4Address'));
    it('flags an ip without a network name', () => bad(form({ networks: [{ name: '', ipv4Address: '10.0.0.5' }] }), 'networks[0].name'));
    it('blank row is fine', () => ok(form({ networks: [{ name: '', ipv4Address: '' }] })));
    it('flags a duplicate network name', () => {
      bad(form({ networks: [{ name: 'a', ipv4Address: '' }, { name: 'a', ipv4Address: '' }] }), 'networks[1].name');
    });
  });

  describe('healthcheck', () => {
    it('is not validated when disabled', () => ok(form({ healthcheck: { ...hc({ test: '', interval: 'x', retries: '-1' }), enabled: false } })));
    it('does not require a test (timing-only is valid compose)', () => ok(form({ healthcheck: hc({ test: '', interval: '5s' }) })));
    for (const d of ['30s', '500ms', '2m', '1h', '1m30s', '1.5s', '2h45m', '0s', '0', '10us', '5ns']) it(`accepts duration ${d}`, () => ok(form({ healthcheck: hc({ interval: d, timeout: d, startPeriod: d }) })));
    for (const d of ['30', 's', '1d', '1 s', 'abc']) {
      it(`rejects duration ${d}`, () => {
        const e = validateForm(form({ healthcheck: hc({ interval: d, timeout: d, startPeriod: d }) }), { original: null });
        expect(Object.keys(e).sort()).toEqual(['healthcheck.interval', 'healthcheck.startPeriod', 'healthcheck.timeout']);
      });
    }
    for (const r of ['0', '3', '10']) it(`accepts retries ${r}`, () => ok(form({ healthcheck: hc({ retries: r }) })));
    for (const r of ['-1', '1.5', 'a', '1e2']) it(`rejects retries ${r}`, () => bad(form({ healthcheck: hc({ retries: r }) }), 'healthcheck.retries'));
    it('skipped when read-only', () => ok(form({ healthcheck: hc({ test: '' }), readOnly: new Set(['healthcheck']) })));
  });
});

describe('mapServerErrors', () => {
  const f = form({
    ports: ['80', '', '8080:80'],
    networks: [{ name: 'back', ipv4Address: '' }, { name: 'lan', ipv4Address: '10.0.0.5' }],
    env: [{ name: '', value: '' }, { name: 'A', value: '' }],
  });
  const m = (field: string, message = 'boom') => mapServerErrors([{ field, message }], f);

  it('maps ports[i] through dropped blank rows', () => {
    expect(m('ports[1]')).toEqual({ 'ports[2]': 'boom' });
    expect(m('ports[0]')).toEqual({ 'ports[0]': 'boom' });
  });
  it('maps networks.<name>.ipv4_address to the row index', () => {
    expect(m('networks.lan.ipv4_address')).toEqual({ 'networks[1].ipv4Address': 'boom' });
    expect(m('networks.back.ipv4_address')).toEqual({ 'networks[0].ipv4Address': 'boom' });
  });
  it('passes simple paths through', () => {
    expect(m('name')).toEqual({ name: 'boom' });
    expect(m('build.context')).toEqual({ 'build.context': 'boom' });
    expect(m('image')).toEqual({ image: 'boom' });
  });
  it('handles dotted service names and ignores other services', () => {
    expect(mapServerErrors([{ field: 'services.a.b.ports[0]', message: 'x' }], f, 'a.b')).toEqual({ 'ports[0]': 'x' });
    expect(mapServerErrors([{ field: 'services.other.ports[0]', message: 'x' }], f, 'web')).toEqual({});
    expect(mapServerErrors([{ field: 'services.web.ports[0]', message: 'x' }], f, 'web')).toEqual({ 'ports[0]': 'x' });
  });
  it('strips a services.<name>. prefix', () => {
    expect(m('services.web.ports[0]')).toEqual({ 'ports[0]': 'boom' });
    expect(m('services.web.networks.lan.ipv4_address')).toEqual({ 'networks[1].ipv4Address': 'boom' });
  });
  it('maps unknown fields and unknown networks to _form', () => {
    expect(m('bogus')).toEqual({ _form: 'boom' });
    expect(m('path')).toEqual({ _form: 'boom' });
    expect(m('networks.nope.ipv4_address')).toEqual({ _form: 'boom' });
    expect(m('ports[9]')).toEqual({ _form: 'boom' });
  });
  it('joins several messages for the same path', () => {
    const e = mapServerErrors([{ field: 'x', message: 'a' }, { field: 'y', message: 'b' }], f);
    expect(e).toEqual({ _form: 'a; b' });
  });
});

describe('fieldOf', () => {
  const cases: [string, string][] = [
    ['name', 'name'],
    ['image', 'image'],
    ['build.context', 'build'],
    ['ports[1]', 'ports'],
    ['volumes[0]', 'volumes'],
    ['env[0].name', 'environment'],
    ['networks[1].ipv4Address', 'networks'],
    ['networks.lan.ipv4_address', 'networks'],
    ['networkMode', 'network_mode'],
    ['limits.memory', 'limits'],
    ['restart', 'restart'],
    ['healthcheck.test', 'healthcheck'],
    ['_form', 'name'],
    ['unknown', 'name'],
  ];
  for (const [path, group] of cases) it(`${path} -> ${group}`, () => expect(fieldOf(path)).toBe(group));
});

describe('round-trip identity of real-world shapes', () => {
  const same = (cfg: ServiceConfig, ro: import('../api/types').Field[] = []) => expect(fromForm(toForm(cfg, ro), cfg)).toEqual(cfg);

  it('keeps image alongside build', () => same({ image: 'me/app:1', build: { context: '.' }, limits: {} }));
  it('keeps exec-form args containing spaces', () =>
    same({ image: 'a', limits: {}, healthcheck: { test: ['CMD', 'sh', '-c', 'curl -f http://x || exit 1'] } }));
  it('keeps folded shell text that has surrounding whitespace', () =>
    same({ image: 'a', limits: {}, healthcheck: { test: ['CMD-SHELL', 'curl -f http://x\n'] } }));
  it('keeps a restart policy outside the simple union', () => same({ image: 'a', limits: {}, restart: 'on-failure:3' }));
  it('keeps unusual but valid values', () =>
    same({
      image: 'a',
      limits: { memory: '1GiB' },
      environment: [{ name: 'discovery.type', value: 'single-node' }],
      healthcheck: { interval: '1m30s', timeout: '1.5s', retries: 0 },
    }));

  it('passes validation', () => {
    const cfg: ServiceConfig = {
      image: 'a',
      limits: { memory: '1GiB' },
      environment: [{ name: 'discovery.type', value: 'single-node' }],
      healthcheck: { interval: '1m30s', timeout: '1.5s' },
    };
    expect(validateForm(toForm(cfg, []), { original: cfg })).toEqual({});
    expect(validateForm(toForm(cfg, []), { original: null })).toEqual({});
  });

  it('does not block unchanged legacy values the stricter rules would reject', () => {
    const cfg: ServiceConfig = { image: 'a', limits: { cpus: '0' }, ports: ['bogus:thing'] };
    const f = toForm(cfg, []);
    expect(validateForm(f, { original: cfg })).toEqual({});
    f.ports.push('bogus2');
    expect(Object.keys(validateForm(f, { original: cfg }))).toEqual(['ports[1]']);
  });

  describe('original config', () => {
    const cfg: ServiceConfig = { image: 'app', limits: { memory: '1G' }, ports: ['8080:80'] };
    it('throws without the original when read-only groups exist, instead of clearing them', () => {
      expect(() => fromForm(toForm(cfg, ['ports']), null)).toThrow(/original/);
    });
    it('a cloned form round-trips when the original is passed', () => {
      const hcCfg: ServiceConfig = { image: 'a', limits: {}, healthcheck: { test: ['CMD', 'sh', '-c', 'curl -f http://x || exit 1'] } };
      for (const copy of [structuredClone(toForm(hcCfg, [])), { ...toForm(hcCfg, []) }]) {
        expect(fromForm(copy, hcCfg)).toEqual(hcCfg);
      }
      expect(fromForm(structuredClone(toForm(cfg, ['ports', 'limits'])), cfg)).toEqual(cfg);
    });
    it('a build with a dockerfile but no context round-trips and validates when unchanged', () => {
      const b: ServiceConfig = { build: { context: '', dockerfile: 'Dockerfile.x' }, limits: {} };
      const f = toForm(b, []);
      expect(fromForm(f, b)).toEqual(b);
      expect(validateForm(f, { original: b })).toEqual({});
      expect(validateForm(f, { original: null })).toHaveProperty(['build.context']);
    });
  });
});
