import type { ContainerRow } from '../api/types';

export type Proto = 'tcp' | 'udp' | 'sctp';

/** The host side of a compose short-syntax port spec. */
export interface HostPorts {
  lo: number;
  hi: number;
  proto: Proto;
}

export interface PublishedPort {
  port: number;
  proto: Proto;
  who: string;
}

export type PortVerdict = { tone: 'danger' | 'warning' | 'success'; text: string };

export interface FormPort {
  /** Card id: names may be empty or repeated, so they cannot identify a row. */
  card: number;
  service: string;
  index: number;
  spec: string;
}

// A typo such as 1-65535 must not stall the render, so only this many ports of a range are checked.
const MAX_RANGE_PORTS = 1024;
const PROTOS: readonly string[] = ['tcp', 'udp', 'sctp'];

function portNumber(s: string): number | undefined {
  if (!/^\d+$/.test(s)) return undefined;
  const n = Number(s);
  return n >= 1 && n <= 65535 ? n : undefined;
}

function portRange(s: string): [number, number] | undefined {
  const [loStr, hiStr, extra] = s.split('-');
  if (extra !== undefined) return undefined;
  const lo = portNumber(loStr);
  if (lo === undefined) return undefined;
  if (hiStr === undefined) return [lo, lo];
  const hi = portNumber(hiStr);
  if (hi === undefined || hi < lo) return undefined;
  return [lo, hi];
}

/**
 * Host side of `[ip:]host[-hi]:container[-hi][/proto]` (IPv6 in brackets, proto default tcp). Mirrors
 * checkPort in lib/compose/form.ts; undefined for anything that has no concrete host port to compare.
 */
export function hostPorts(rawSpec: string): HostPorts | undefined {
  const spec = rawSpec.trim();
  if (spec === '' || spec.includes('$')) return undefined;
  let body = spec;
  let proto = 'tcp';
  const slash = spec.lastIndexOf('/');
  if (slash >= 0) {
    body = spec.slice(0, slash);
    proto = spec.slice(slash + 1).toLowerCase() || 'tcp';
    if (!PROTOS.includes(proto)) return undefined;
  }
  let rest = body;
  if (rest.startsWith('[')) {
    const end = rest.indexOf(']:');
    if (end < 0) return undefined;
    rest = rest.slice(end + 2);
  } else if (rest.split(':').length - 1 >= 2) {
    const last = rest.lastIndexOf(':');
    const prev = rest.lastIndexOf(':', last - 1);
    rest = rest.slice(prev + 1);
  }
  const parts = rest.split(':');
  if (parts.length !== 2) return undefined;
  const [hostStr, containerStr] = parts;
  if (portRange(containerStr) === undefined) return undefined;
  const host = portRange(hostStr);
  if (host === undefined) return undefined;
  return { lo: host[0], hi: host[1], proto: proto as Proto };
}

/** Host ports already published by running containers, from entries like `0.0.0.0:8080->80/tcp` or `[::]:53->53/udp`. */
export function publishedPorts(rows: ContainerRow[]): PublishedPort[] {
  const out: PublishedPort[] = [];
  for (const row of rows) {
    for (const entry of row.ports) {
      const m = /^(?:\[[^\]]*\]|[^:]*):(\d+)->\d+\/(\w+)$/.exec(entry);
      if (!m) continue;
      const port = portNumber(m[1]);
      const proto = m[2].toLowerCase();
      if (port === undefined || !PROTOS.includes(proto)) continue;
      out.push({ port, proto: proto as Proto, who: row.stack || row.name });
    }
  }
  return out;
}

export function portVerdict(
  spec: string,
  own: { card: number; service: string; index: number },
  formPorts: FormPort[],
  published: PublishedPort[] | undefined,
): PortVerdict | undefined {
  const host = hostPorts(spec);
  if (!host || !published) return undefined;

  const ownAt = formPorts.findIndex((p) => p.card === own.card && p.index === own.index);
  const earlier = (ownAt < 0 ? formPorts : formPorts.slice(0, ownAt)).flatMap((p) => {
    const h = hostPorts(p.spec);
    return h && h.proto === host.proto ? [{ service: p.service, lo: h.lo, hi: h.hi }] : [];
  });

  const last = Math.min(host.hi, host.lo + MAX_RANGE_PORTS - 1);
  for (let port = host.lo; port <= last; port++) {
    const used = published.find((p) => p.port === port && p.proto === host.proto);
    if (used) return { tone: 'danger', text: `Port ${port} is used by ${used.who}` };
    const dup = earlier.find((p) => p.lo <= port && port <= p.hi);
    if (dup) return { tone: 'warning', text: `Port ${port} is also published by ${dup.service || 'another service'} in this stack` };
  }
  return { tone: 'success', text: host.lo === host.hi ? `Port ${host.lo} is free` : `Ports ${host.lo}-${host.hi} are free` };
}
