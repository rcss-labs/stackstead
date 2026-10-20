import { useMemo } from 'react';
import { portVerdict, publishedPorts } from '$lib/stacks/portCheck';
import type { FormPort, PortVerdict } from '$lib/stacks/portCheck';
import { useContainerList } from '../containers/useContainerList';
import type { Card } from './useCreateForm';

/**
 * Advisory live port check: per card, a note for each port row against the host ports containers already
 * publish and against the earlier ports of this form. Silent until the containers list has loaded, and
 * silent for good if it fails (the server re-checks on create).
 */
export function usePortCheck(cards: Card[]): (cardId: number) => (spec: string, index: number) => PortVerdict | undefined {
  const { rows } = useContainerList();
  const published = useMemo(() => rows && publishedPorts(rows), [rows]);
  const formPorts: FormPort[] = cards.flatMap((card) => card.form.ports.map((spec, index) => ({ service: card.name, index, spec })));
  return (cardId) => {
    const card = cards.find((candidate) => candidate.id === cardId);
    return (spec, index) => (card ? portVerdict(spec, { service: card.name, index }, formPorts, published) : undefined);
  };
}
