import type { StackDetail } from '$lib/api/types';

export type DetailItem =
  | { kind: 'service'; name: string }
  | { kind: 'compose' }
  | { kind: 'operations' }
  | { kind: 'none' };

export type ServiceTab = 'overview' | 'metrics' | 'config' | 'dockerfile';

export interface DetailLocation {
  item: DetailItem;
  serviceTab: ServiceTab;
  logsRequested: boolean;
  logService: string | undefined;
  op: string | undefined;
}

export function missingComposeFile(parseError: string | undefined): boolean {
  return !!parseError && /no such file|not exist|not found|cannot find/i.test(parseError);
}

export function resolveDetailLocation(
  query: URLSearchParams,
  detail: Pick<StackDetail, 'services' | 'parseError'> | undefined,
): DetailLocation {
  if (!detail) return { item: { kind: 'none' }, serviceTab: 'overview', logsRequested: false, logService: undefined, op: undefined };

  const tab = query.get('tab');
  const names = detail.services.map((s) => s.name);
  const requested = query.get('service') ?? '';
  const known = names.includes(requested) ? requested : undefined;
  const op = query.get('op') || undefined;

  let item: DetailItem;
  if (detail.parseError) item = missingComposeFile(detail.parseError) || tab === 'operations' ? { kind: 'operations' } : { kind: 'compose' };
  else if (tab === 'compose') item = { kind: 'compose' };
  else if (tab === 'operations') item = { kind: 'operations' };
  else if (known ?? names[0]) item = { kind: 'service', name: (known ?? names[0])! };
  else item = { kind: 'none' };

  return {
    item,
    serviceTab: tab === 'metrics' || tab === 'config' || tab === 'dockerfile' ? tab : 'overview',
    logsRequested: tab === 'logs' && !detail.parseError,
    logService: known,
    op: item.kind === 'operations' ? op : undefined,
  };
}

export function detailHref(name: string, params: { tab?: string; service?: string; op?: string }): string {
  const q = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) if (value) q.set(key, value);
  const qs = q.toString();
  return `/stacks/${encodeURIComponent(name)}${qs ? `?${qs}` : ''}`;
}

export function itemHref(name: string, item: DetailItem, serviceTab: ServiceTab = 'overview'): string {
  switch (item.kind) {
    case 'service':
      return detailHref(name, { service: item.name, tab: serviceTab === 'overview' ? undefined : serviceTab });
    case 'compose':
      return detailHref(name, { tab: 'compose' });
    case 'operations':
      return detailHref(name, { tab: 'operations' });
    case 'none':
      return detailHref(name, {});
  }
}
