// Pure derivation of the overview's issue cards from alerts and storage reclaimable. No React, no clock.
import type { Alert, StorageOverview } from '../api/types';
import { formatBytes } from '../format';

export type IssueAction =
  { label: string; kind: 'navigate'; href: string } | { label: string; kind: 'act'; stack: string; action: 'up' | 'restart' };

export interface IssueCard {
  id: string;
  kind: string;
  tone: 'danger' | 'warning' | 'info';
  pulse: boolean;
  where?: { label: string; href: string; mono: boolean };
  since?: string;
  title: string;
  message: string;
  primary?: IssueAction;
  secondary?: IssueAction;
}

/** Banner kinds: shown by the app shell, not as cards. */
export const EXCLUDED_ALERT_KINDS: ReadonlySet<string> = new Set(['docker_disconnected', 'metrics_degraded']);

/** The storage card appears only when at least 1 GiB can be reclaimed. */
export const RECLAIMABLE_CARD_MIN = 1024 ** 3;

const CONTAINER_KINDS: ReadonlySet<string> = new Set(['exited', 'unhealthy', 'restart_loop']);

const stackHref = (stack: string) => `/stacks/${encodeURIComponent(stack)}`;
const containerHref = (container: string) => `/containers/${encodeURIComponent(container)}`;

export function reclaimableTotal(storage: StorageOverview | undefined): number {
  if (!storage) return 0;
  return [storage.images, storage.containers, storage.volumes, storage.buildCache].reduce((sum, k) => sum + (k.reclaimable ?? 0), 0);
}

function alertTitle(alert: Alert): string {
  const subject = alert.service ?? alert.container ?? 'container';
  switch (alert.kind) {
    case 'exited':
      return `${subject} exited`;
    case 'unhealthy':
      return `${subject} is unhealthy`;
    case 'restart_loop':
      return `${subject} is restart looping`;
    case 'port_clash':
      return 'Port conflict';
    case 'override':
      return 'Override file ignored';
    case 'parse_error':
      return 'Compose file cannot be parsed';
    default:
      return alert.kind.replace(/_/g, ' ');
  }
}

function stackPrimary(alert: Alert, stack: string): IssueAction | undefined {
  switch (alert.kind) {
    case 'exited':
      return { label: 'Up stack', kind: 'act', stack, action: 'up' };
    case 'unhealthy':
    case 'restart_loop':
      return { label: 'Restart stack', kind: 'act', stack, action: 'restart' };
    case 'parse_error':
      return {
        label: 'Open compose',
        kind: 'navigate',
        href: `${stackHref(stack)}?tab=compose`,
      };
    default:
      return { label: 'Open stack', kind: 'navigate', href: stackHref(stack) };
  }
}

function alertCard(alert: Alert, index: number): IssueCard {
  const card: IssueCard = {
    id: [alert.kind, alert.stack ?? '', alert.container ?? '', alert.since, index].join('|'),
    kind: alert.kind,
    tone: alert.severity,
    pulse: alert.kind === 'unhealthy',
    since: alert.since,
    title: alertTitle(alert),
    message: alert.message,
  };
  if (alert.stack) {
    card.where = {
      label: alert.stack,
      href: stackHref(alert.stack),
      mono: true,
    };
    card.primary = stackPrimary(alert, alert.stack);
    if (alert.service) {
      card.secondary = {
        label: 'Open logs',
        kind: 'navigate',
        href: `${stackHref(alert.stack)}?tab=logs&service=${encodeURIComponent(alert.service)}`,
      };
    }
  } else if (alert.container) {
    const href = containerHref(alert.container);
    card.where = { label: alert.container, href, mono: true };
    card.primary = { label: 'Open container', kind: 'navigate', href };
  }
  return card;
}

function storageCard(storage: StorageOverview): IssueCard {
  const parts: [number | null, string][] = [
    [storage.images.reclaimable, 'dangling images'],
    [storage.containers.reclaimable, 'container layers'],
    [storage.volumes.reclaimable, 'unused volumes'],
    [storage.buildCache.reclaimable, 'build cache'],
  ];
  return {
    id: 'reclaimable',
    kind: 'reclaimable',
    tone: 'info',
    pulse: false,
    where: { label: 'storage', href: '/storage', mono: false },
    title: `${formatBytes(reclaimableTotal(storage))} can be freed`,
    message: parts
      .filter((p): p is [number, string] => p[0] !== null && p[0] > 0)
      .map(([size, label]) => `${formatBytes(size)} ${label}`)
      .join(' · '),
    primary: { label: 'Review cleanup', kind: 'navigate', href: '/storage' },
  };
}

/** undefined while alerts are not loaded (or failed): the overview then says nothing. */
export function issueCards(input: { alerts: Alert[] | undefined; storage: StorageOverview | undefined }): IssueCard[] | undefined {
  if (!input.alerts) return undefined;
  const cards = input.alerts
    .map((alert, index) => ({ alert, index }))
    .filter(({ alert }) => !EXCLUDED_ALERT_KINDS.has(alert.kind))
    .map(({ alert, index }) => alertCard(alert, index));
  if (input.storage && reclaimableTotal(input.storage) >= RECLAIMABLE_CARD_MIN) cards.push(storageCard(input.storage));
  return cards;
}

/** Distinct services with a container problem in this stack, in alert order. */
export function failingServices(alerts: Alert[] | undefined, stack: string): string[] {
  const services = new Set<string>();
  for (const alert of alerts ?? []) {
    if (alert.stack === stack && alert.service && CONTAINER_KINDS.has(alert.kind)) services.add(alert.service);
  }
  return [...services];
}
