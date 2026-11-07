import { useMemo } from 'react';
import { hostPorts, portVerdict, publishedPorts } from '$lib/stacks/portCheck';
import type { FormPort, PublishedPort } from '$lib/stacks/portCheck';
import { useContainerList } from '../containers/useContainerList';
import type { PortNote } from '../stacks/ServiceForm';
import type { Card } from './useCreateForm';
import { firstFreePortAfter } from './freePort';

// The first port, or "lo-hi" for a range, above `after` where `width` consecutive ports are all untaken.
function firstFreeRangeAfter(taken: ReadonlySet<number>, after: number, width: number): string | undefined {
  for (let lo = firstFreePortAfter(taken, after); lo !== undefined && lo + width - 1 <= 65535; lo = firstFreePortAfter(taken, lo)) {
    let clash = 0;
    for (let offset = 1; offset < width && !clash; offset++) if (taken.has(lo + offset)) clash = lo + offset;
    if (!clash) return width === 1 ? `${lo}` : `${lo}-${lo + width - 1}`;
    lo = clash;
  }
  return undefined;
}

// Same bound as the range check in lib/stacks/portCheck: a typo such as 1-65535 must not stall the render.
const MAX_RANGE_PORTS = 1024;

function takenPorts(spec: string, published: PublishedPort[], formPorts: FormPort[]): { from: number; width: number; taken: Set<number> } | undefined {
  const own = hostPorts(spec);
  if (!own) return undefined;
  const taken = new Set<number>();
  for (const p of published) if (p.proto === own.proto) taken.add(p.port);
  for (const p of formPorts) {
    const host = hostPorts(p.spec);
    if (!host || host.proto !== own.proto) continue;
    for (let port = host.lo; port <= Math.min(host.hi, host.lo + MAX_RANGE_PORTS - 1); port++) taken.add(port);
  }
  return { from: own.hi, width: own.hi - own.lo + 1, taken };
}

/**
 * Advisory live port check: per card, a note for each port row against the host ports containers already
 * publish and against the earlier ports of this form. A clash with a running container also suggests the
 * next free port. Silent until the containers list has loaded, and silent for good if it fails (the server
 * re-checks on create).
 */
export function usePortCheck(cards: Card[]): (cardId: number) => (spec: string, index: number) => PortNote | undefined {
  const { rows } = useContainerList();
  const published = useMemo(() => rows && publishedPorts(rows), [rows]);
  const formPorts: FormPort[] = cards.flatMap((card) => card.form.ports.map((spec, index) => ({ card: card.id, service: card.name, index, spec })));
  return (cardId) => {
    const card = cards.find((candidate) => candidate.id === cardId);
    return (spec, index) => {
      if (!card) return undefined;
      const verdict = portVerdict(spec, { card: card.id, service: card.name, index }, formPorts, published);
      if (!verdict || verdict.tone !== 'danger' || !published) return verdict;
      const search = takenPorts(spec, published, formPorts);
      const free = search && firstFreeRangeAfter(search.taken, search.from, search.width);
      return free === undefined ? verdict : { ...verdict, suggestion: `${free} not used by a running container` };
    };
  };
}
