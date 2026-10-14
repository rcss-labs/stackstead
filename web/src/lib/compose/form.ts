import type { Field, FieldError, ServiceConfig } from '../api/types';

export interface ServiceForm {
  source: 'image' | 'build';
  image: string;
  buildContext: string;
  dockerfile: string;
  ports: string[];
  volumes: string[];
  env: { name: string; value: string }[];
  networks: { name: string; ipv4Address: string }[];
  networkMode: string;
  cpus: string;
  memory: string;
  restart: '' | 'no' | 'always' | 'unless-stopped' | 'on-failure';
  healthcheck: {
    enabled: boolean;
    test: string;
    interval: string;
    timeout: string;
    startPeriod: string;
    retries: string;
  };
  readOnly: Set<Field>;
}

export type FormErrors = { [path: string]: string };

const NAME_RE = /^[a-z0-9][a-z0-9_-]{0,62}$/;
// Same wording as the server so a client-side and a server-side name error read alike.
const NAME_MSG =
  "name must start with a lowercase letter or digit, contain only lowercase letters, digits, '-' and '_', and be at most 63 characters";
const ENV_NAME_RE = /^[A-Za-z_][A-Za-z0-9_]*$/;
const MEMORY_RE = /^\d+(\.\d+)?[kKmMgG]?[bB]?$/;
const CPUS_RE = /^(\d+(\.\d+)?|\.\d+)$/;
const DURATION_RE = /^\d+(ms|s|m|h)$/;
const RETRIES_RE = /^\d+$/;

/** The unedited config, kept off the enumerable keys so read-only groups can be copied through on save. */
const ORIG = '_orig';

export function emptyForm(): ServiceForm {
  return {
    source: 'image',
    image: '',
    buildContext: '',
    dockerfile: '',
    ports: [],
    volumes: [],
    env: [],
    networks: [],
    networkMode: '',
    cpus: '',
    memory: '',
    restart: '',
    healthcheck: { enabled: false, test: '', interval: '', timeout: '', startPeriod: '', retries: '' },
    readOnly: new Set(),
  };
}

function healthcheckToText(test: string[] | undefined): string {
  if (!test || test.length === 0) return '';
  if (test[0] === 'CMD-SHELL') return test.length === 2 ? test[1] : test.join(' ');
  return test.join(' ');
}

export function toForm(cfg: ServiceConfig, readOnly: Field[]): ServiceForm {
  const f = emptyForm();
  f.source = cfg.build ? 'build' : 'image';
  f.image = cfg.image ?? '';
  f.buildContext = cfg.build?.context ?? '';
  f.dockerfile = cfg.build?.dockerfile ?? '';
  f.ports = [...(cfg.ports ?? [])];
  f.volumes = [...(cfg.volumes ?? [])];
  f.env = (cfg.environment ?? []).map((e) => ({ name: e.name, value: e.value }));
  f.networks = (cfg.networks ?? []).map((n) => ({ name: n.name, ipv4Address: n.ipv4Address ?? '' }));
  f.networkMode = cfg.networkMode ?? '';
  f.cpus = cfg.limits?.cpus ?? '';
  f.memory = cfg.limits?.memory ?? '';
  f.restart = (cfg.restart ?? '') as ServiceForm['restart'];
  if (cfg.healthcheck) {
    const h = cfg.healthcheck;
    f.healthcheck = {
      enabled: true,
      test: healthcheckToText(h.test),
      interval: h.interval ?? '',
      timeout: h.timeout ?? '',
      startPeriod: h.startPeriod ?? '',
      retries: h.retries === undefined ? '' : String(h.retries),
    };
  }
  f.readOnly = new Set(readOnly);
  Object.defineProperty(f, ORIG, { value: structuredClone(cfg), enumerable: false, writable: true });
  return f;
}

/** Exec form when the text starts with CMD, shell form otherwise, so the result is always valid compose. */
function textToHealthcheckTest(text: string): string[] {
  const t = text.trim();
  const [first, ...rest] = t.split(/\s+/);
  if (first === 'CMD-SHELL') return ['CMD-SHELL', t.slice(first.length).trim()];
  if (first === 'CMD' || first === 'NONE') return [first, ...rest];
  return ['CMD-SHELL', t];
}

export function fromForm(f: ServiceForm): ServiceConfig {
  const orig: ServiceConfig | undefined = (f as unknown as Record<string, ServiceConfig | undefined>)[ORIG];
  const ro = (g: Field) => f.readOnly.has(g);
  const cfg: ServiceConfig = { limits: {} };
  const set = <K extends keyof ServiceConfig>(k: K, v: ServiceConfig[K] | undefined) => {
    if (v !== undefined) cfg[k] = v;
  };

  if (ro('image') || ro('build')) {
    set('image', orig?.image);
    set('build', orig?.build);
  } else if (f.source === 'build') {
    const context = f.buildContext.trim();
    if (context) cfg.build = f.dockerfile.trim() ? { context, dockerfile: f.dockerfile.trim() } : { context };
  } else if (f.image.trim()) {
    cfg.image = f.image.trim();
  }

  const nonBlank = (xs: string[]) => xs.map((x) => x.trim()).filter((x) => x !== '');
  const ports = ro('ports') ? orig?.ports : nonBlank(f.ports);
  if (ports?.length) cfg.ports = ports;
  const volumes = ro('volumes') ? orig?.volumes : nonBlank(f.volumes);
  if (volumes?.length) cfg.volumes = volumes;

  const env = ro('environment') ? orig?.environment : f.env.filter((e) => e.name !== '' || e.value !== '');
  if (env?.length) cfg.environment = env.map((e) => ({ name: e.name, value: e.value }));

  const nets = ro('networks')
    ? orig?.networks
    : f.networks
        .filter((n) => n.name.trim() !== '')
        .map((n) => (n.ipv4Address.trim() ? { name: n.name.trim(), ipv4Address: n.ipv4Address.trim() } : { name: n.name.trim() }));
  if (nets?.length) cfg.networks = nets;

  const mode = ro('network_mode') ? orig?.networkMode : f.networkMode.trim();
  if (mode) cfg.networkMode = mode;

  if (ro('limits')) {
    cfg.limits = { ...(orig?.limits ?? {}) };
  } else {
    if (f.cpus.trim()) cfg.limits.cpus = f.cpus.trim();
    if (f.memory.trim()) cfg.limits.memory = f.memory.trim();
  }

  const restart = ro('restart') ? orig?.restart : f.restart;
  if (restart) cfg.restart = restart;

  if (ro('healthcheck')) {
    set('healthcheck', orig?.healthcheck);
  } else if (f.healthcheck.enabled) {
    const h = f.healthcheck;
    const out: NonNullable<ServiceConfig['healthcheck']> = {};
    if (h.test.trim()) out.test = textToHealthcheckTest(h.test);
    if (h.interval.trim()) out.interval = h.interval.trim();
    if (h.timeout.trim()) out.timeout = h.timeout.trim();
    if (h.startPeriod.trim()) out.startPeriod = h.startPeriod.trim();
    if (h.retries.trim() && RETRIES_RE.test(h.retries.trim())) out.retries = Number(h.retries.trim());
    cfg.healthcheck = out;
  }
  return cfg;
}

// --- validation -----------------------------------------------------------

function isIPv4(s: string): boolean {
  const parts = s.split('.');
  return parts.length === 4 && parts.every((p) => /^(0|[1-9]\d{0,2})$/.test(p) && Number(p) <= 255);
}

function isIPv6(s: string): boolean {
  if (!s.includes(':') || !/^[0-9a-fA-F:.]+$/.test(s)) return false;
  const doubles = s.split('::').length - 1;
  if (doubles > 1 || s.includes(':::')) return false;
  let groups = s.split(':').filter((g) => g !== '');
  const last = groups[groups.length - 1];
  let extra = 0;
  if (last?.includes('.')) {
    if (!isIPv4(last)) return false;
    groups = groups.slice(0, -1);
    extra = 2;
  }
  if (!groups.every((g) => /^[0-9a-fA-F]{1,4}$/.test(g))) return false;
  const count = groups.length + extra;
  return doubles === 1 ? count < 8 : count === 8;
}

function portNumber(s: string): number | null {
  if (!/^\d+$/.test(s)) return null;
  const n = Number(s);
  return n >= 1 && n <= 65535 ? n : null;
}

function portRange(s: string): [number, number] | string {
  const [loStr, hiStr, extra] = s.split('-');
  if (extra !== undefined) return `${JSON.stringify(s)} is not a port between 1 and 65535`;
  const lo = portNumber(loStr);
  if (lo === null) return `${JSON.stringify(loStr)} is not a port between 1 and 65535`;
  if (hiStr === undefined) return [lo, lo];
  const hi = portNumber(hiStr);
  if (hi === null) return `${JSON.stringify(hiStr)} is not a port between 1 and 65535`;
  if (hi < lo) return `range ${JSON.stringify(s)} is descending`;
  return [lo, hi];
}

/** Mirrors the server's short-syntax port parser (internal/stacks/validate.go). Returns a message or null. */
function checkPort(spec: string): string | null {
  const bad = (m: string) => `invalid port "${spec}": ${m}`;
  if (spec.includes('$')) return null;
  let body = spec;
  const slash = spec.lastIndexOf('/');
  if (slash >= 0) {
    body = spec.slice(0, slash);
    const proto = spec.slice(slash + 1).toLowerCase();
    if (!['', 'tcp', 'udp', 'sctp'].includes(proto)) return bad(`unknown protocol "${proto}"`);
  }
  let ip = '';
  let rest = body;
  if (rest.startsWith('[')) {
    const end = rest.indexOf(']:');
    if (end < 0) return bad('malformed IPv6 address');
    ip = rest.slice(1, end);
    rest = rest.slice(end + 2);
  } else if (rest.split(':').length - 1 >= 2) {
    const last = rest.lastIndexOf(':');
    const prev = rest.lastIndexOf(':', last - 1);
    ip = rest.slice(0, prev);
    rest = rest.slice(prev + 1);
  }
  const parts = rest.split(':');
  let hostStr = '';
  let containerStr: string;
  if (parts.length === 1) {
    containerStr = parts[0];
    if (ip !== '') return bad('expected [ip]:host:container');
  } else if (parts.length === 2) {
    [hostStr, containerStr] = parts;
  } else {
    return bad("too many ':' separators");
  }
  if (ip !== '' && !isIPv4(ip) && !isIPv6(ip)) return bad(`invalid IP "${ip}"`);
  const c = portRange(containerStr);
  if (typeof c === 'string') return bad(c);
  if (hostStr === '' || hostStr === '0') return null;
  const h = portRange(hostStr);
  if (typeof h === 'string') return bad(h);
  if (c[1] !== c[0] && c[1] - c[0] !== h[1] - h[0]) return bad('host and container ranges differ in length');
  return null;
}

export function validateForm(f: ServiceForm, opts: { name?: string; requireName?: boolean }): FormErrors {
  const errors: FormErrors = {};
  const ro = (g: Field) => f.readOnly.has(g);

  if (opts.requireName || opts.name !== undefined) {
    const name = opts.name ?? '';
    if (name === '' && opts.requireName) errors.name = 'name is required';
    else if (name !== '' && !NAME_RE.test(name)) errors.name = NAME_MSG;
  }

  if (!ro('image') && !ro('build')) {
    if (f.source === 'image') {
      if (!f.image.trim()) errors.image = 'image is required';
    } else if (!f.buildContext.trim()) {
      errors['build.context'] = 'build context is required';
    }
  }

  if (!ro('ports')) {
    f.ports.forEach((p, i) => {
      if (p.trim() === '') return;
      const msg = checkPort(p.trim());
      if (msg) errors[`ports[${i}]`] = msg;
    });
  }

  if (!ro('environment')) {
    const seen = new Set<string>();
    f.env.forEach((e, i) => {
      if (e.name === '' && e.value === '') return;
      const at = `env[${i}].name`;
      if (e.name === '') errors[at] = 'variable name is required';
      else if (!ENV_NAME_RE.test(e.name)) errors[at] = 'name must start with a letter or underscore and contain only letters, digits and underscores';
      else if (seen.has(e.name)) errors[at] = `duplicate variable ${e.name}`;
      seen.add(e.name);
    });
  }

  if (!ro('networks')) {
    const seen = new Set<string>();
    f.networks.forEach((n, i) => {
      const name = n.name.trim();
      if (name === '') return;
      if (seen.has(name)) errors[`networks[${i}].name`] = `duplicate network ${name}`;
      seen.add(name);
      const ip = n.ipv4Address.trim();
      if (ip !== '' && !isIPv4(ip)) errors[`networks[${i}].ipv4Address`] = `"${ip}" is not a valid IPv4 address`;
    });
  }

  if (!ro('limits')) {
    const cpus = f.cpus.trim();
    if (cpus !== '' && (!CPUS_RE.test(cpus) || Number(cpus) <= 0)) errors['limits.cpus'] = 'cpus must be a positive number, e.g. 0.5';
    const memory = f.memory.trim();
    if (memory !== '' && !MEMORY_RE.test(memory)) errors['limits.memory'] = 'memory must look like 512M or 1.5G';
  }

  if (!ro('healthcheck') && f.healthcheck.enabled) {
    const h = f.healthcheck;
    if (!h.test.trim()) errors['healthcheck.test'] = 'a health check command is required';
    const durations: [string, string][] = [
      ['interval', h.interval],
      ['timeout', h.timeout],
      ['startPeriod', h.startPeriod],
    ];
    for (const [key, v] of durations) {
      if (v.trim() !== '' && !DURATION_RE.test(v.trim())) errors[`healthcheck.${key}`] = 'use a duration like 30s, 500ms, 2m or 1h';
    }
    if (h.retries.trim() !== '' && !RETRIES_RE.test(h.retries.trim())) errors['healthcheck.retries'] = 'retries must be a whole number, 0 or more';
  }
  return errors;
}

// --- server errors --------------------------------------------------------

/** Server list index -> form row index, since blank rows are dropped by fromForm. */
function rowIndex(rows: boolean[], serverIndex: number): number | null {
  let n = -1;
  for (let i = 0; i < rows.length; i++) {
    if (rows[i] && ++n === serverIndex) return i;
  }
  return null;
}

function mapPath(field: string, f: ServiceForm): string | null {
  const path = field.replace(/^services\.[^.]+\./, '');
  let m = /^(ports|volumes)\[(\d+)\]$/.exec(path);
  if (m) {
    const rows = f[m[1] as 'ports' | 'volumes'].map((p) => p.trim() !== '');
    const i = rowIndex(rows, Number(m[2]));
    return i === null ? null : `${m[1]}[${i}]`;
  }
  m = /^networks\.(.+)\.ipv4_address$/.exec(path);
  if (m) {
    const i = f.networks.findIndex((n) => n.name.trim() === m![1]);
    return i < 0 ? null : `networks[${i}].ipv4Address`;
  }
  m = /^environment\[(\d+)\](?:\.(name|value))?$/.exec(path);
  if (m) {
    const i = rowIndex(f.env.map((e) => e.name !== '' || e.value !== ''), Number(m[1]));
    return i === null ? null : `env[${i}].${m[2] ?? 'name'}`;
  }
  if (path === 'name' || path === 'image' || path === 'restart') return path;
  if (path === 'build.context' || path === 'build.dockerfile') return path;
  if (path === 'network_mode') return 'networkMode';
  if (path === 'limits.cpus' || path === 'limits.memory') return path;
  m = /^healthcheck\.(test|interval|timeout|start_period|startPeriod|retries)$/.exec(path);
  if (m) return `healthcheck.${m[1] === 'start_period' ? 'startPeriod' : m[1]}`;
  return null;
}

export function mapServerErrors(fields: FieldError[], f: ServiceForm): FormErrors {
  const errors: FormErrors = {};
  for (const fe of fields) {
    const path = mapPath(fe.field, f) ?? '_form';
    errors[path] = errors[path] ? `${errors[path]}; ${fe.message}` : fe.message;
  }
  return errors;
}

/** Which field group a form path belongs to; anything not tied to a group (including `_form`) reports 'name'. */
export function fieldOf(path: string): Field | 'name' {
  const head = /^[A-Za-z_]+/.exec(path)?.[0] ?? '';
  switch (head) {
    case 'image':
    case 'build':
    case 'ports':
    case 'volumes':
    case 'networks':
    case 'limits':
    case 'restart':
    case 'healthcheck':
      return head;
    case 'env':
    case 'environment':
      return 'environment';
    case 'networkMode':
    case 'network_mode':
      return 'network_mode';
    default:
      return 'name';
  }
}
