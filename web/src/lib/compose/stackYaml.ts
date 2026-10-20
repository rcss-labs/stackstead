import { Document, isMap, isScalar, isSeq, parseDocument, Pair, visit, YAMLMap } from 'yaml';
import type { Field } from '../api/types';
import { emptyForm, fromForm, type ServiceForm } from './form';

/** Plain JSON data: compose keys the form does not model, carried along unchanged. */
export type Extra = Record<string, unknown>;

export interface StackCard {
  name: string;
  form: ServiceForm;
  extra: Extra;
}

type Plain = Record<string, unknown>;

const isPlain = (v: unknown): v is Plain => typeof v === 'object' && v !== null && !Array.isArray(v);
const isScalarValue = (v: unknown): v is string | number | boolean => ['string', 'number', 'boolean'].includes(typeof v);

function deepEqual(a: unknown, b: unknown): boolean {
  if (a === b) return true;
  if (Array.isArray(a)) return Array.isArray(b) && a.length === b.length && a.every((x, i) => deepEqual(x, b[i]));
  if (isPlain(a)) {
    if (!isPlain(b)) return false;
    const keys = Object.keys(a);
    return keys.length === Object.keys(b).length && keys.every((k) => Object.prototype.hasOwnProperty.call(b, k) && deepEqual(a[k], b[k]));
  }
  return false;
}

// --- form -> compose keys -------------------------------------------------

/** Compose keys for what the form holds, in the server's key order. Read-only groups render nothing here (extra supplies them). */
function formToCompose(form: ServiceForm, name: string): Plain {
  // A managed Dockerfile needs the service name; an unnamed card still has to render so switching modes never throws.
  const cfg = fromForm(form, { limits: {} }, name || 'service');
  const out: Plain = {};
  if (cfg.image !== undefined) out.image = cfg.image;
  if (cfg.build) out.build = cfg.build.dockerfile ? { context: cfg.build.context, dockerfile: cfg.build.dockerfile } : cfg.build.context;
  if (cfg.restart) out.restart = cfg.restart;
  if (cfg.ports) out.ports = cfg.ports;
  if (cfg.volumes) out.volumes = cfg.volumes;
  if (cfg.environment) out.environment = Object.fromEntries(cfg.environment.map((e) => [e.name, e.value]));
  if (cfg.networks) {
    out.networks = cfg.networks.some((n) => n.ipv4Address)
      ? Object.fromEntries(cfg.networks.map((n) => [n.name, n.ipv4Address ? { ipv4_address: n.ipv4Address } : null]))
      : cfg.networks.map((n) => n.name);
  }
  if (cfg.networkMode) out.network_mode = cfg.networkMode;
  if (cfg.limits.cpus || cfg.limits.memory) {
    const limits: Plain = {};
    if (cfg.limits.cpus) limits.cpus = cfg.limits.cpus;
    if (cfg.limits.memory) limits.memory = cfg.limits.memory;
    out.deploy = { resources: { limits } };
  }
  if (cfg.healthcheck) {
    const h = cfg.healthcheck;
    const hc: Plain = {};
    if (h.test) hc.test = h.test;
    if (h.interval) hc.interval = h.interval;
    if (h.timeout) hc.timeout = h.timeout;
    if (h.startPeriod) hc.start_period = h.startPeriod;
    if (h.retries !== undefined) hc.retries = h.retries;
    out.healthcheck = hc;
  }
  return out;
}

/** Deep merge: maps merge, anything else keeps the form's value; keys only the extra has are appended. */
function mergeInto(formValue: Plain, extra: Plain): Plain {
  const out: Plain = { ...formValue };
  for (const [key, extraValue] of Object.entries(extra)) {
    if (!(key in out)) {
      out[key] = extraValue;
      continue;
    }
    const formPart = out[key];
    if (isPlain(formPart) && isPlain(extraValue)) out[key] = mergeInto(formPart, extraValue);
    else if (key === 'networks' && Array.isArray(formPart) && isPlain(extraValue)) {
      // Form lists network names, extra holds their details: render the names as a mapping and merge.
      out[key] = mergeInto(Object.fromEntries(formPart.map((n) => [String(n), null])), extraValue);
    } else if (formPart === null && extraValue !== undefined) out[key] = extraValue;
  }
  return out;
}

export function joinService(card: StackCard): Record<string, unknown> {
  return mergeInto(formToCompose(card.form, card.name), card.extra);
}

// --- compose keys -> form -------------------------------------------------

const ABSENT = Symbol('absent');

function envListToMap(list: unknown[]): Plain | null {
  const out: Plain = {};
  for (const item of list) {
    if (typeof item !== 'string') return null;
    const eq = item.indexOf('=');
    if (eq <= 0) return null; // a bare name means pass-through (null), which the form cannot hold
    const name = item.slice(0, eq);
    if (name in out) return null;
    out[name] = item.slice(eq + 1);
  }
  return out;
}

/**
 * Spelling differences that mean the same thing to compose, folded into the shape the form renders, so a value is
 * not pushed to extra over them. Empty collections count as absent (a formatting-only loss).
 */
function normalize(key: string, value: unknown): unknown {
  switch (key) {
    case 'ports':
    case 'volumes':
      if (!Array.isArray(value)) return value;
      return value.length === 0 ? ABSENT : value.map((v) => (isScalarValue(v) ? String(v) : v));
    case 'environment': {
      const map = Array.isArray(value) ? envListToMap(value) ?? value : value;
      if (Array.isArray(map)) return map.length === 0 ? ABSENT : map;
      if (!isPlain(map)) return map;
      if (Object.keys(map).length === 0) return ABSENT;
      return Object.fromEntries(Object.entries(map).map(([k, v]) => [k, isScalarValue(v) ? String(v) : v]));
    }
    case 'networks': {
      if (Array.isArray(value)) return value.length === 0 ? ABSENT : value;
      if (!isPlain(value)) return value;
      const empty = (v: unknown) => v === null || (isPlain(v) && Object.keys(v).length === 0);
      if (Object.keys(value).length === 0) return ABSENT;
      if (Object.values(value).every(empty)) return Object.keys(value);
      return Object.fromEntries(Object.entries(value).map(([k, v]) => [k, empty(v) ? null : v]));
    }
    case 'healthcheck':
      return isPlain(value) && typeof value.test === 'string' ? { ...value, test: ['CMD-SHELL', value.test] } : value;
    case 'build':
      return typeof value === 'string' ? { context: value } : value;
    default:
      return value;
  }
}

const trimmedNonEmpty = (v: unknown): v is string => typeof v === 'string' && v !== '' && v === v.trim();

/** Offers a form that might hold `value`; the caller verifies by rendering it back. Null when the shape is hopeless. */
function candidate(key: string, value: unknown, name: string, dockerfiles: Record<string, string>): ServiceForm | null {
  const f = emptyForm();
  switch (key) {
    case 'image':
      if (!trimmedNonEmpty(value)) return null;
      f.image = value;
      return f;
    case 'build': {
      const build = typeof value === 'string' ? { context: value } : value;
      if (!isPlain(build) || !trimmedNonEmpty(build.context)) return null;
      const dockerfile = build.dockerfile;
      if (dockerfile !== undefined && !trimmedNonEmpty(dockerfile)) return null;
      const managed = build.context === './' + name && dockerfile === undefined && Object.keys(build).length === 1;
      if (managed && dockerfiles[name] !== undefined) {
        f.source = 'dockerfile';
        f.dockerfileText = dockerfiles[name];
        return f;
      }
      f.source = 'build';
      f.buildContext = build.context;
      f.dockerfile = dockerfile ?? '';
      return f;
    }
    case 'ports':
    case 'volumes':
      if (!Array.isArray(value) || !value.every(isScalarValue)) return null;
      f[key] = value.map(String);
      return f;
    case 'environment':
      if (!isPlain(value) || !Object.values(value).every(isScalarValue)) return null;
      f.env = Object.entries(value).map(([n, v]) => ({ name: n, value: String(v) }));
      return f;
    case 'networks':
      if (Array.isArray(value)) {
        if (!value.every(trimmedNonEmpty)) return null;
        f.networks = value.map((n) => ({ name: n, ipv4Address: '' }));
        return f;
      }
      if (!isPlain(value)) return null;
      f.networks = Object.entries(value).map(([n, v]) => ({
        name: n,
        ipv4Address: isPlain(v) && typeof v.ipv4_address === 'string' ? v.ipv4_address : '',
      }));
      return f;
    case 'network_mode':
      if (!trimmedNonEmpty(value)) return null;
      f.networkMode = value;
      return f;
    case 'restart':
      if (typeof value !== 'string') return null;
      f.restart = value;
      return f;
    case 'healthcheck': {
      if (!isPlain(value)) return null;
      const text = (v: unknown) => (typeof v === 'string' ? v : '');
      const test = value.test;
      f.healthcheck = {
        enabled: true,
        test: Array.isArray(test) && test.every((t) => typeof t === 'string') ? (test[0] === 'CMD-SHELL' && test.length === 2 ? test[1] : test.join(' ')) : text(test),
        interval: text(value.interval),
        timeout: text(value.timeout),
        startPeriod: text(value.start_period),
        retries: typeof value.retries === 'number' ? String(value.retries) : '',
      };
      return f;
    }
    default:
      return null;
  }
}

/** Compose keys that become one form group, and the readOnly groups a failure of that key locks. */
const GROUPS: { keys: string[]; locks: Field[] }[] = [
  { keys: ['image', 'build'], locks: ['image', 'build'] }, // fromForm treats the two as one unit
  { keys: ['ports'], locks: ['ports'] },
  { keys: ['volumes'], locks: ['volumes'] },
  { keys: ['environment'], locks: ['environment'] },
  { keys: ['networks'], locks: ['networks'] },
  { keys: ['network_mode'], locks: ['network_mode'] },
  { keys: ['restart'], locks: ['restart'] },
  { keys: ['healthcheck'], locks: ['healthcheck'] },
];

function applyGroup(into: ServiceForm, from: ServiceForm, keys: string[]): void {
  // Only the fields a key owns are copied, so a failed neighbour never leaks half-parsed state.
  if (keys.includes('image')) Object.assign(into, { image: from.image });
  if (keys.includes('build')) Object.assign(into, { source: from.source, buildContext: from.buildContext, dockerfile: from.dockerfile, dockerfileText: from.dockerfileText });
  if (keys.includes('ports')) into.ports = from.ports;
  if (keys.includes('volumes')) into.volumes = from.volumes;
  if (keys.includes('environment')) into.env = from.env;
  if (keys.includes('networks')) into.networks = from.networks;
  if (keys.includes('network_mode')) into.networkMode = from.networkMode;
  if (keys.includes('restart')) into.restart = from.restart;
  if (keys.includes('healthcheck')) into.healthcheck = from.healthcheck;
}

/** Decision 13: a key goes into the form only if rendering the form back reproduces the parsed value. */
export function splitService(name: string, value: unknown, dockerfiles: Record<string, string>): { form: ServiceForm; extra: Extra } {
  const service: Plain = isPlain(value) ? value : {};
  const form = emptyForm();
  const moved = new Set<string>();

  for (const group of GROUPS) {
    const inDoc = group.keys.filter((k) => k in service);
    const normalized: Plain = {};
    const present: string[] = [];
    for (const k of inDoc) {
      const n = normalize(k, service[k]);
      if (n === ABSENT) moved.add(k);
      else {
        normalized[k] = n;
        present.push(k);
      }
    }
    if (present.length === 0) continue;
    // Merge the candidates for every present key of the group into one form, then verify each key by rendering.
    const trial = emptyForm();
    let ok = true;
    for (const key of present) {
      const c = candidate(key, normalized[key], name, dockerfiles);
      if (!c) {
        ok = false;
        break;
      }
      applyGroup(trial, c, [key]);
    }
    if (ok) {
      const rendered = formToCompose(trial, name);
      ok = present.every((k) => k in rendered && deepEqual(normalize(k, rendered[k]), normalized[k]));
      // A key the form renders but the document does not have would add settings out of nowhere.
      ok = ok && group.keys.every((k) => k in service || !(k in rendered));
    }
    if (ok) {
      applyGroup(form, trial, present);
      present.forEach((k) => moved.add(k));
    } else {
      group.locks.forEach((l) => form.readOnly.add(l));
    }
  }

  const extra: Extra = {};
  for (const [key, v] of Object.entries(service)) {
    if (moved.has(key)) continue;
    if (key === 'deploy') {
      const rest = splitDeploy(v, form);
      if (rest !== undefined) extra.deploy = rest;
      continue;
    }
    extra[key] = v;
  }
  return { form, extra };
}

/** Moves deploy.resources.limits.{cpus,memory} into the form; returns what stays in extra.deploy (undefined when nothing). */
function splitDeploy(deploy: unknown, form: ServiceForm): unknown {
  if (!isPlain(deploy)) {
    form.readOnly.add('limits');
    return deploy;
  }
  const resources = deploy.resources;
  if (resources === undefined) return deploy;
  if (!isPlain(resources) || (resources.limits !== undefined && !isPlain(resources.limits))) {
    form.readOnly.add('limits');
    return deploy;
  }
  const limits = resources.limits;
  if (!limits) return deploy;

  // Numbers become strings. If either limit is unrepresentable neither moves, so extra never repeats a limit the form holds.
  const amount = (v: unknown) => (typeof v === 'number' && Number.isFinite(v) ? String(v) : trimmedNonEmpty(v) ? v : undefined);
  const cpus = 'cpus' in limits ? amount(limits.cpus) : undefined;
  const memory = 'memory' in limits ? amount(limits.memory) : undefined;
  if (('cpus' in limits && cpus === undefined) || ('memory' in limits && memory === undefined)) {
    form.readOnly.add('limits');
    return deploy;
  }
  const rest: Plain = { ...limits };
  if (cpus !== undefined) {
    form.cpus = cpus;
    delete rest.cpus;
  }
  if (memory !== undefined) {
    form.memory = memory;
    delete rest.memory;
  }
  if (Object.keys(rest).length === Object.keys(limits).length) return deploy; // nothing moved

  const newResources: Plain = { ...resources };
  if (Object.keys(rest).length > 0) newResources.limits = rest;
  else delete newResources.limits;
  const newDeploy: Plain = { ...deploy };
  if (Object.keys(newResources).length > 0) newDeploy.resources = newResources;
  else delete newDeploy.resources;
  return Object.keys(newDeploy).length > 0 ? newDeploy : undefined;
}

// --- document ---------------------------------------------------------------

export function stackToYaml(cards: StackCard[], extra: Extra): string {
  const doc = new Document();
  const create = (v: unknown) => doc.createNode(v, { aliasDuplicateObjects: false });
  // Built as pairs, not an object, so two cards with the same name stay visible (and fail to parse back) instead of one vanishing.
  const services = new YAMLMap();
  for (const card of cards) {
    const node = create(joinService(card));
    if (isMap(node)) {
      const ports = node.get('ports', true);
      if (isSeq(ports)) for (const item of ports.items) if (isScalar(item) && typeof item.value === 'string') item.type = 'QUOTE_DOUBLE';
    }
    services.items.push(new Pair(create(card.name), node));
  }
  const root = new YAMLMap();
  root.items.push(new Pair(create('services'), services));
  for (const [key, value] of Object.entries(extra)) {
    if (key === 'services' || key === 'name') continue;
    root.items.push(new Pair(create(key), create(value)));
  }
  doc.contents = root;
  return doc.toString({ indent: 2, lineWidth: 0 });
}

export type ParseResult =
  | { ok: true; name?: string; cards: StackCard[]; extra: Extra }
  | { ok: false; message: string; line?: number };

const NEEDS_SERVICES = 'A stack needs a services: section';

function hasNonFinite(v: unknown): boolean {
  if (typeof v === 'number') return !Number.isFinite(v);
  if (Array.isArray(v)) return v.some(hasNonFinite);
  if (isPlain(v)) return Object.values(v).some(hasNonFinite);
  return false;
}

/**
 * Integers are parsed as bigint so nothing above 2^53 is rounded. A safe integer goes back to a number; a larger one
 * becomes its exact decimal text (from the bigint, never from a float), so env values, labels and extras keep every digit.
 */
const fromBigInt = (n: bigint): number | string => (n >= BigInt(Number.MIN_SAFE_INTEGER) && n <= BigInt(Number.MAX_SAFE_INTEGER) ? Number(n) : n.toString());

/**
 * YAML 1.2 core reads a plain 0755 as decimal 755, but the Go server (yaml.v3) reads legacy octal as 493. The create request
 * carries JSON values the server re-renders, so plain scalars are read the way Go reads them. Hex (0x1F) already agrees. 1_000 does not: Go reads it as 1000 while this library keeps the text "1_000"; that is left as is.
 * Visiting the whole document also covers anchored values, merge keys and top-level x-* blocks. Quoted scalars are untouched.
 */
function readNumbers(doc: Document.Parsed): void {
  visit(doc, {
    Scalar(_key, node) {
      if (typeof node.value !== 'bigint') return;
      const octal = node.type === 'PLAIN' && typeof node.source === 'string' ? /^([-+]?)0([0-7]+)$/.exec(node.source) : null;
      node.value = octal ? fromBigInt((octal[1] === '-' ? -1n : 1n) * BigInt('0o' + octal[2])) : fromBigInt(node.value);
    },
  });
}

export function yamlToStack(text: string, dockerfiles: Record<string, string>, maxServices: number): ParseResult {
  const doc = parseDocument(text, { merge: true, uniqueKeys: true, intAsBigInt: true });
  const err = doc.errors[0];
  if (err) {
    // yaml appends " at line N, column M:" and a source excerpt to the message; the line is reported separately.
    return { ok: false, message: err.message.replace(/ at line \d+, column \d+:[\s\S]*$/, ''), line: err.linePos?.[0].line };
  }
  let root: unknown;
  try {
    // JSON round trip: expands aliases into independent copies (no shared references that would be written back as anchors).
    readNumbers(doc);
    const js = doc.toJS({ maxAliasCount: 1000 });
    if (hasNonFinite(js)) return { ok: false, message: 'Values such as .inf and .nan cannot be kept' };
    root = js === undefined ? undefined : JSON.parse(JSON.stringify(js));
  } catch (e) {
    return { ok: false, message: e instanceof Error ? e.message : 'Could not read the document' };
  }
  if (!isPlain(root) || !isPlain(root.services)) return { ok: false, message: NEEDS_SERVICES };

  const names = Object.keys(root.services);
  if (names.length > maxServices) return { ok: false, message: `A stack can have at most ${maxServices} services.` };

  const cards: StackCard[] = [];
  for (const name of names) {
    const value = root.services[name];
    if (value !== null && !isPlain(value)) return { ok: false, message: `Service ${name} must be a mapping` };
    const { form, extra } = splitService(name, value ?? {}, dockerfiles);
    cards.push({ name, form, extra });
  }

  let stackName: string | undefined;
  if (root.name !== undefined) {
    if (typeof root.name !== 'string') return { ok: false, message: 'The stack name: must be text' };
    stackName = root.name;
  }
  const extra: Extra = {};
  for (const [key, v] of Object.entries(root)) if (key !== 'services' && key !== 'name') extra[key] = v;
  return { ok: true, ...(stackName !== undefined ? { name: stackName } : {}), cards, extra };
}

export function extraCount(extra: Extra): number {
  return Object.keys(extra).length;
}
