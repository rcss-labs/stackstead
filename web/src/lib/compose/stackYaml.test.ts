import { describe, expect, it } from 'vitest';
import { parse } from 'yaml';
import { emptyForm } from './form';
import { extraCount, joinService, splitService, stackToYaml, yamlToStack, type StackCard } from './stackYaml';

const parsed = (text: string) => parse(text) as Record<string, unknown>;

function mustParse(text: string, dockerfiles: Record<string, string> = {}) {
  const r = yamlToStack(text, dockerfiles, 20);
  if (!r.ok) throw new Error(`parse failed: ${r.message}`);
  return r;
}

/** YAML -> cards -> YAML -> cards -> YAML, returning each text. */
function roundTrip(text: string): [string, string] {
  const first = mustParse(text);
  const y1 = stackToYaml(first.cards, first.extra);
  const second = mustParse(y1);
  const y2 = stackToYaml(second.cards, second.extra);
  return [y1, y2];
}

describe('stackYaml', () => {
  it('round trip keeps every form field', () => {
    const form = emptyForm();
    form.image = 'nginx:1.27';
    form.ports = ['8080:80', '443'];
    form.volumes = ['data:/data'];
    form.env = [{ name: 'A', value: '1' }, { name: 'EMPTY', value: '' }];
    form.networks = [{ name: 'lan', ipv4Address: '10.0.0.5' }, { name: 'other', ipv4Address: '' }];
    form.networkMode = '';
    form.cpus = '0.5';
    form.memory = '512M';
    form.restart = 'unless-stopped';
    form.healthcheck = { enabled: true, test: 'curl -f http://localhost', interval: '30s', timeout: '5s', startPeriod: '10s', retries: '3' };
    const card: StackCard = { name: 'web', form, extra: {} };
    const result = mustParse(stackToYaml([card], {}));
    expect(result.cards).toEqual([card]);
    expect(result.extra).toEqual({});
  });

  it('round trip keeps network mode and a plain network list', () => {
    const form = emptyForm();
    form.image = 'a';
    form.networks = [{ name: 'one', ipv4Address: '' }, { name: 'two', ipv4Address: '' }];
    const card: StackCard = { name: 'a', form, extra: {} };
    expect(mustParse(stackToYaml([card], {})).cards).toEqual([card]);
    const host = emptyForm();
    host.image = 'a';
    host.networkMode = 'host';
    const hostCard: StackCard = { name: 'a', form: host, extra: {} };
    expect(mustParse(stackToYaml([hostCard], {})).cards).toEqual([hostCard]);
  });

  it('round trip keeps unknown keys per service and top-level', () => {
    const text = `services:
  web:
    image: nginx
    labels: {a: b}
    logging: {driver: json-file}
    command: [sleep, "1"]
    depends_on: [db]
  db:
    image: postgres
networks:
  lan: {driver: bridge}
volumes:
  data: {}
secrets:
  s: {file: ./s}
configs:
  c: {file: ./c}
x-common:
  restart: always
`;
    const [y1, y2] = roundTrip(text);
    expect(parsed(y1)).toEqual(parsed(text));
    expect(parsed(y2)).toEqual(parsed(y1));
    const r = mustParse(text);
    expect(Object.keys(r.extra)).toEqual(['networks', 'volumes', 'secrets', 'configs', 'x-common']);
    expect(Object.keys(r.cards[0].extra)).toEqual(['labels', 'logging', 'command', 'depends_on']);
  });

  it('round trip keeps values the form cannot hold', () => {
    const text = `services:
  a:
    image: x
    healthcheck:
      test: ["CMD", "curl", "-f", "http://a b"]
    environment:
      KEY:
    ports:
      - target: 80
        published: 8080
    networks:
      lan:
        ipv4_address: 10.0.0.2
        aliases: [a]
    deploy:
      resources:
        limits: {cpus: "0.5", memory: 512M}
      placement:
        constraints: [node.role == manager]
`;
    const [y1, y2] = roundTrip(text);
    expect(parsed(y1)).toEqual(parsed(text));
    expect(parsed(y2)).toEqual(parsed(text));
    const card = mustParse(text).cards[0];
    expect(card.extra.healthcheck).toEqual({ test: ['CMD', 'curl', '-f', 'http://a b'] });
    expect(card.extra.environment).toEqual({ KEY: null });
    expect(card.extra.ports).toEqual([{ target: 80, published: 8080 }]);
    expect(card.extra.networks).toEqual({ lan: { ipv4_address: '10.0.0.2', aliases: ['a'] } });
    expect([...card.form.readOnly].sort()).toEqual(['environment', 'healthcheck', 'networks', 'ports']);
    expect(card.form.cpus).toBe('0.5');
  });

  it('deploy splits limits from the rest', () => {
    const { form, extra } = splitService(
      'a',
      { image: 'x', deploy: { replicas: 2, resources: { limits: { cpus: '1', memory: '1G' }, reservations: { memory: '256M' } } } },
      {},
    );
    expect(form.cpus).toBe('1');
    expect(form.memory).toBe('1G');
    expect(form.readOnly.size).toBe(0);
    expect(extra).toEqual({ deploy: { replicas: 2, resources: { reservations: { memory: '256M' } } } });
    expect(joinService({ name: 'a', form, extra })).toEqual({
      image: 'x',
      deploy: { resources: { limits: { cpus: '1', memory: '1G' }, reservations: { memory: '256M' } }, replicas: 2 },
    });
  });

  it('numeric limits become strings in the form', () => {
    const { form, extra } = splitService('a', { image: 'x', deploy: { resources: { limits: { cpus: 0.5, memory: 512 } } } }, {});
    expect(form.cpus).toBe('0.5');
    expect(form.memory).toBe('512');
    expect(form.readOnly.size).toBe(0);
    expect(extra).toEqual({});
  });

  it('mixed numeric and string limits survive a round trip', () => {
    for (const limits of ['{cpus: 0.5, memory: 512M}', '{cpus: "0.5", memory: 512}']) {
      const text = `services:\n  a:\n    image: x\n    deploy:\n      resources:\n        limits: ${limits}\n`;
      const [y1, y2] = roundTrip(text);
      const limitsOf = (y: string) => (parsed(y).services as any).a.deploy.resources.limits;
      expect(Object.keys(limitsOf(y1)).sort()).toEqual(['cpus', 'memory']);
      expect(limitsOf(y2)).toEqual(limitsOf(y1));
      const card = mustParse(text).cards[0];
      expect(card.form.readOnly.size).toBe(0);
      expect(card.extra).toEqual({});
    }
  });

  it('unrepresentable limits lock the group and move neither limit to the form', () => {
    const text = 'services:\n  a:\n    image: x\n    deploy:\n      resources:\n        limits: {cpus: true, memory: 512M}\n';
    const card = mustParse(text).cards[0];
    expect(card.form.cpus).toBe('');
    expect(card.form.memory).toBe('');
    expect(card.form.readOnly.has('limits')).toBe(true);
    expect(card.extra).toEqual({ deploy: { resources: { limits: { cpus: true, memory: '512M' } } } });
    const [y1, y2] = roundTrip(text);
    expect(parsed(y1)).toEqual(parsed(text));
    expect(parsed(y2)).toEqual(parsed(text));
  });

  it('an environment list of K=V goes into the form', () => {
    const text = 'services:\n  a:\n    image: x\n    environment:\n      - A=b\n      - C=d=e\n      - EMPTY=\n';
    const card = mustParse(text).cards[0];
    expect(card.form.env).toEqual([{ name: 'A', value: 'b' }, { name: 'C', value: 'd=e' }, { name: 'EMPTY', value: '' }]);
    expect(card.form.readOnly.size).toBe(0);
    expect(card.extra).toEqual({});
    const [y1, y2] = roundTrip(text);
    expect(parsed(y2)).toEqual(parsed(y1));
  });

  it('a bare name in an environment list stays in extra', () => {
    const text = 'services:\n  a:\n    image: x\n    environment:\n      - A=b\n      - PASS\n';
    const card = mustParse(text).cards[0];
    expect(card.extra.environment).toEqual(['A=b', 'PASS']);
    expect(card.form.readOnly.has('environment')).toBe(true);
    expect(parsed(roundTrip(text)[1])).toEqual(parsed(text));
  });

  it('a healthcheck test string goes into the form', () => {
    const text = 'services:\n  a:\n    image: x\n    healthcheck:\n      test: curl -f http://x\n      retries: 2\n';
    const card = mustParse(text).cards[0];
    expect(card.form.healthcheck.test).toBe('curl -f http://x');
    expect(card.form.readOnly.size).toBe(0);
    const [y1, y2] = roundTrip(text);
    expect((parsed(y1).services as any).a.healthcheck.test).toEqual(['CMD-SHELL', 'curl -f http://x']);
    expect(parsed(y2)).toEqual(parsed(y1));
  });

  it('a networks mapping of null or empty values goes into the form as a list', () => {
    const text = 'services:\n  a:\n    image: x\n    networks:\n      lan:\n      other: {}\n';
    const card = mustParse(text).cards[0];
    expect(card.form.networks).toEqual([{ name: 'lan', ipv4Address: '' }, { name: 'other', ipv4Address: '' }]);
    expect(card.form.readOnly.size).toBe(0);
    const [y1, y2] = roundTrip(text);
    expect((parsed(y1).services as any).a.networks).toEqual(['lan', 'other']);
    expect(parsed(y2)).toEqual(parsed(y1));
  });

  it('empty ports, volumes and environment count as absent', () => {
    const card = mustParse('services:\n  a:\n    image: x\n    ports: []\n    volumes: []\n    environment: {}\n').cards[0];
    expect(card.form.readOnly.size).toBe(0);
    expect(card.extra).toEqual({});
    expect(card.form.ports).toEqual([]);
  });

  it('plain numbers are read the way the Go server reads them', () => {
    const text = `services:
  a:
    image: x
    ports: ["8080:80", 0x1F]
    environment:
      MODE: 0755
      H: 0x1F
      US: 1_000
      Q: "0755"
    deploy:
      resources:
        limits: {memory: 0755}
`;
    const card = mustParse(text).cards[0];
    expect(card.form.env).toEqual([
      { name: 'MODE', value: '493' },
      { name: 'H', value: '31' },
      { name: 'US', value: '1_000' },
      { name: 'Q', value: '0755' },
    ]);
    expect(card.form.ports).toEqual(['8080:80', '31']);
    expect(card.form.memory).toBe('493');
    expect(card.form.readOnly.size).toBe(0);
    const [y1, y2] = roundTrip(text);
    const again = mustParse(y1).cards[0];
    expect(again.form).toEqual(card.form);
    expect(parsed(y2)).toEqual(parsed(y1));
  });

  it('legacy octal is read through anchors and merge keys and in extras', () => {
    const text = 'x-e: &e {MODE: 0755}\nx-plain: {m: 0755, q: "0755"}\nservices:\n  a:\n    image: x\n    environment:\n      <<: *e\n';
    const r = mustParse(text);
    expect(r.cards[0].form.env).toEqual([{ name: 'MODE', value: '493' }]);
    expect(r.extra).toEqual({ 'x-e': { MODE: 493 }, 'x-plain': { m: 493, q: '0755' } });
    expect(parsed(stackToYaml(r.cards, r.extra))['x-e']).toEqual({ MODE: 493 });
  });

  it('numeric ports and env values become strings', () => {
    const card = mustParse('services:\n  a:\n    image: x\n    ports: [80, "81:81"]\n    environment:\n      PORT: 8080\n      DEBUG: true\n').cards[0];
    expect(card.form.ports).toEqual(['80', '81:81']);
    expect(card.form.env).toEqual([{ name: 'PORT', value: '8080' }, { name: 'DEBUG', value: 'true' }]);
    expect(card.extra).toEqual({});
  });

  it('ports are written double-quoted', () => {
    const form = emptyForm();
    form.image = 'x';
    form.ports = ['8080:80', '22'];
    const y = stackToYaml([{ name: 'a', form, extra: {} }], {});
    expect(y).toContain('- "8080:80"');
    expect(y).toContain('- "22"');
  });

  it('anchors and merge keys are expanded', () => {
    const text = `x-base: &b
  restart: always
services:
  a:
    <<: *b
    image: x
`;
    const r = mustParse(text);
    expect(r.cards[0].form.restart).toBe('always');
    expect(r.cards[0].extra).toEqual({});
    expect(r.extra).toEqual({ 'x-base': { restart: 'always' } });
    expect(stackToYaml(r.cards, r.extra)).not.toMatch(/[&*]/);
  });

  it('shared aliased values are written out in full', () => {
    const r = mustParse('x-l: &l {a: b}\nservices:\n  a:\n    image: x\n    labels: *l\n  b:\n    image: y\n    labels: *l\n');
    const y = stackToYaml(r.cards, r.extra);
    expect(y).not.toMatch(/[&*]/);
    expect(parsed(y).services).toEqual({ a: { image: 'x', labels: { a: 'b' } }, b: { image: 'y', labels: { a: 'b' } } });
  });

  it('syntax error reports message and line', () => {
    const r = yamlToStack('services:\n  a:\n    image: x\n   restart: always\n', {}, 20);
    expect(r.ok).toBe(false);
    if (!r.ok) {
      expect(r.line).toBe(4);
      expect(r.message).toBe('All mapping items must start at the same column');
      expect(r.message).not.toMatch(/\n/);
      expect(r.message.length).toBeGreaterThan(0);
    }
  });

  it('duplicate keys are refused', () => {
    const r = yamlToStack('services:\n  a:\n    image: x\n    image: y\n', {}, 20);
    expect(r.ok).toBe(false);
  });

  it('missing services is refused', () => {
    for (const text of ['{}', 'services: []', 'just a scalar', '', 'services:\n', '- a\n']) {
      const r = yamlToStack(text, {}, 20);
      expect(r, text).toEqual({ ok: false, message: 'A stack needs a services: section' });
    }
  });

  it('more than 20 services is refused', () => {
    const many = Array.from({ length: 21 }, (_, i) => `  s${i}:\n    image: x\n`).join('');
    expect(yamlToStack(`services:\n${many}`, {}, 20)).toEqual({ ok: false, message: 'A stack can have at most 20 services.' });
    const twenty = Array.from({ length: 20 }, (_, i) => `  s${i}:\n    image: x\n`).join('');
    expect(yamlToStack(`services:\n${twenty}`, {}, 20).ok).toBe(true);
  });

  it('name feeds the result, not extra', () => {
    const r = mustParse('name: shop\nservices:\n  a:\n    image: x\n');
    expect(r.name).toBe('shop');
    expect(r.extra).toEqual({});
    expect(mustParse('services:\n  a:\n    image: x\n').name).toBeUndefined();
  });

  it('a null service counts as empty and renders {}', () => {
    const r = mustParse('services:\n  a:\n');
    expect(r.cards).toHaveLength(1);
    expect(r.cards[0].extra).toEqual({});
    expect(stackToYaml(r.cards, r.extra)).toContain('a: {}');
  });

  it('a managed build with Dockerfile text becomes Write a Dockerfile', () => {
    const text = 'services:\n  web:\n    build: ./web\n    image: stackstead/x:latest\n  api:\n    build:\n      context: ./api\n';
    const r = mustParse(text, { web: 'FROM alpine\n', api: 'FROM debian\n' });
    expect(r.cards[0].form.source).toBe('dockerfile');
    expect(r.cards[0].form.dockerfileText).toBe('FROM alpine\n');
    expect(r.cards[0].form.image).toBe('stackstead/x:latest');
    expect(r.cards[1].form.source).toBe('dockerfile');
    expect(r.cards[1].form.dockerfileText).toBe('FROM debian\n');
    expect(r.cards[0].extra).toEqual({});
    expect(parsed(stackToYaml(r.cards, r.extra)).services).toEqual({
      web: { image: 'stackstead/x:latest', build: './web' },
      api: { build: './api' },
    });
  });

  it('a managed build without text becomes Existing build', () => {
    const card = mustParse('services:\n  web:\n    build: ./web\n').cards[0];
    expect(card.form.source).toBe('build');
    expect(card.form.buildContext).toBe('./web');
    expect(card.form.dockerfileText).toBe('');
  });

  it('a build with args stays in extra and locks image and build', () => {
    const card = mustParse('services:\n  web:\n    image: x\n    build:\n      context: .\n      args: {A: "1"}\n').cards[0];
    expect(card.extra).toEqual({ image: 'x', build: { context: '.', args: { A: '1' } } });
    expect(card.form.readOnly.has('build')).toBe(true);
    expect(card.form.readOnly.has('image')).toBe(true);
  });

  it('a build with context and dockerfile fits the form', () => {
    const card = mustParse('services:\n  web:\n    build:\n      context: ./src\n      dockerfile: Dockerfile.dev\n').cards[0];
    expect(card.form.source).toBe('build');
    expect(card.form.buildContext).toBe('./src');
    expect(card.form.dockerfile).toBe('Dockerfile.dev');
    expect(card.extra).toEqual({});
  });

  it('writes keys in the server order', () => {
    const form = emptyForm();
    form.image = 'x';
    form.restart = 'always';
    form.ports = ['1'];
    form.volumes = ['v:/v'];
    form.env = [{ name: 'A', value: 'b' }];
    form.networks = [{ name: 'n', ipv4Address: '' }];
    form.networkMode = 'bridge';
    form.cpus = '1';
    form.healthcheck = { ...form.healthcheck, enabled: true, test: 'true' };
    expect(Object.keys(joinService({ name: 'a', form, extra: { labels: { a: 'b' } } }))).toEqual([
      'image', 'restart', 'ports', 'volumes', 'environment', 'networks', 'network_mode', 'deploy', 'healthcheck', 'labels',
    ]);
  });

  it('writes services first and top-level extras after', () => {
    const form = emptyForm();
    form.image = 'x';
    const y = stackToYaml([{ name: 'a', form, extra: {} }], { volumes: { data: {} }, 'x-c': 1 });
    expect(Object.keys(parsed(y))).toEqual(['services', 'volumes', 'x-c']);
  });

  it('extraCount counts top-level keys', () => {
    expect(extraCount({})).toBe(0);
    expect(extraCount({ a: 1, b: { c: 2 } })).toBe(2);
  });

  it('non-finite numbers are refused rather than dropped', () => {
    expect(yamlToStack('services:\n  a:\n    image: x\n    weight: .inf\n', {}, 20).ok).toBe(false);
  });
});
