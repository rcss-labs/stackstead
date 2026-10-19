import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router';
import { api } from '$lib/api/client';
import type { ContainerDetail } from '$lib/api/types';
import { containerLed } from '$lib/containers/list';
import { formatBytes, formatPercent } from '$lib/format';
import { imagePath } from '$lib/storage/storage';
import { debounce } from '$lib/stores/debounce';
import { changes } from '$lib/stores/live';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { CopyButton } from '../components/CopyButton';
import { EmptyState } from '../components/EmptyState';
import { Led } from '../components/Led';
import { PanelLabel } from '../components/PanelLabel';
import { RelativeTime } from '../components/RelativeTime';
import { Spinner } from '../components/Spinner';
import { StatusBadge } from '../components/StatusBadge';
import { Tabs } from '../components/Tabs';
import { Tag } from '../components/Tag';
import { LogConsole } from '../detail/LogConsole';
import { useIsMobile } from '../hooks/useIsMobile';
import { ContainerActions, ContainerConfirmDialog } from './ContainerActions';
import styles from './ContainerDetailScreen.module.css';
import { useContainerAction } from './useContainerAction';
import { useContainerList } from './useContainerList';

const TABS = [
  { id: 'details', label: 'Details' },
  { id: 'logs', label: 'Logs' },
];

const composeFirst = (labels: Record<string, string>) =>
  Object.entries(labels).sort(([a], [b]) => {
    const ca = a.startsWith('com.docker.compose.') ? 0 : 1;
    const cb = b.startsWith('com.docker.compose.') ? 0 : 1;
    return ca - cb || a.localeCompare(b);
  });

// The limit comes from the container's own config; the stats limit is host RAM for unlimited containers.
function memoryText(used: number | null, limit: number | null): string {
  if (used === null) return '—';
  return limit ? `${formatBytes(used)} / ${formatBytes(limit)}` : formatBytes(used);
}

const unknownTime = (iso: string | null) => !iso || iso.startsWith('0001-');

export function ContainerDetailScreen({ name }: { name: string }) {
  const [query] = useSearchParams();
  const navigate = useNavigate();
  const mobile = useIsMobile();
  const requested = query.get('tab');
  const tab = TABS.find((t) => t.id === requested)?.id ?? 'details';

  // The page owns its list: it resolves the name and supplies the live CPU/memory values.
  const list = useContainerList();
  const actions = useContainerAction((_row, fresh) => {
    if (fresh) list.patch(fresh);
    list.refresh();
  });
  const { rows, error: listError } = list;
  const row = rows?.find((c) => c.name === name);
  // Distinguishes "never existed" from "was here and is now gone".
  const seen = useRef(false);
  useEffect(() => {
    if (row) seen.current = true;
  }, [row]);

  useEffect(() => {
    document.title = `${name} · Stackstead`;
  }, [name]);

  const [detail, setDetail] = useState<ContainerDetail | undefined>();
  const [detailError, setDetailError] = useState('');
  const [logsOpen, setLogsOpen] = useState(true);
  // A response only lands if no newer load (or id change) started after it.
  const seq = useRef(0);
  const loadDetail = useCallback(async (id: string) => {
    const mine = ++seq.current;
    try {
      const d = await api.container(id);
      if (mine !== seq.current) return;
      setDetail(d);
      setDetailError('');
    } catch (e) {
      if (mine !== seq.current) return;
      setDetailError(e instanceof Error ? e.message : String(e));
    }
  }, []);

  // A recreated container keeps its name but not its id: drop the old details and load the new ones.
  // Keyed on the id primitive so a list refresh that changes nothing does not refetch the details.
  const rowId = row?.id;
  const loadedId = useRef<string | undefined>(undefined);
  useEffect(() => {
    if (rowId !== loadedId.current) {
      loadedId.current = rowId;
      setDetail(undefined);
      setDetailError('');
      seq.current++;
    }
    if (rowId && tab === 'details') void loadDetail(rowId);
  }, [rowId, tab, loadDetail]);

  // The refetch always reads the current row and tab, whichever render scheduled it.
  const current = useRef({ row, tab });
  useEffect(() => {
    current.current = { row, tab };
  });
  const refetch = useMemo(
    () =>
      debounce(() => {
        const { row: r, tab: t } = current.current;
        if (r && t === 'details') void loadDetail(r.id);
      }),
    [loadDetail],
  );
  useEffect(() => {
    let first = true; // the store replays its latest value on subscribe
    const unsubscribe = changes.subscribe((c) => {
      if (first) {
        first = false;
        return;
      }
      if (!c) return;
      const project = current.current.row?.project;
      if (c.resync) refetch.flush();
      else if (!project || c.project === project) refetch.trigger();
    });
    return () => {
      unsubscribe();
      refetch.cancel();
    };
  }, [refetch]);

  if (rows === undefined) {
    if (!listError) return <Spinner />;
    return (
      <>
        <Banner kind="danger" title="Could not load the container">
          {listError}
        </Banner>
        <p>
          <Button variant="secondary" onClick={list.refresh}>
            Retry
          </Button>
        </p>
      </>
    );
  }
  if (!row) {
    const gone = seen.current;
    return (
      <EmptyState
        title={gone ? 'This container no longer exists' : 'Container not found'}
        description={gone ? `"${name}" was removed.` : `There is no container named "${name}".`}
      >
        <Link to="/containers">Back to containers</Link>
      </EmptyState>
    );
  }

  const led = containerLed(row);
  const fullId = detail?.id ?? row.id;
  const image = imagePath(row.imageId);
  const stackUrl = `/stacks/${encodeURIComponent(row.stack)}`;

  return (
    <section className={styles.page}>
      {listError && (
        <Banner kind="warning" title="Container list out of date">
          Showing the last known state; {listError}
        </Banner>
      )}
      <header className={styles.head}>
        <div className={styles.row1}>
          <Led tone={led.tone} pulse={led.pulse} size={10} />
          <h1 className={styles.title} data-mobile={mobile ? 'true' : undefined}>
            {row.name}
          </h1>
          <StatusBadge status={row.state} />
          {row.health !== 'none' && (
            <span className={styles.health} data-health={row.health}>
              <i className={styles.dot} />
              {row.health}
            </span>
          )}
          <p className={styles.meta}>
            {image ? (
              <Link to={image} className={styles.mono}>
                {row.image}
              </Link>
            ) : (
              <span className={styles.mono}>{row.image}</span>
            )}{' '}
            ·{' '}
            {row.managed === 'stack' ? (
              <>
                stack <Link to={stackUrl}>{row.stack}</Link>
              </>
            ) : row.managed === 'compose' ? (
              <>
                project <span className={styles.mono}>{row.project}</span> <Tag>unregistered</Tag>
              </>
            ) : (
              'not in a stack'
            )}
          </p>
          <div className={styles.actions} role="group" aria-label="Container actions">
            {row.managed === 'stack' ? (
              <>
                <Link to={stackUrl} className={styles.openStack}>
                  Open stack
                </Link>
                <span className={styles.note}>Managed by stack {row.stack}; use the stack&apos;s actions.</span>
              </>
            ) : (
              <ContainerActions row={row} size="md" reason={actions.reasonFor(row)} onRequest={(action) => actions.request(row, action)} />
            )}
          </div>
        </div>
        <div className={styles.row2}>
          <span className={styles.mono} title={fullId}>
            {fullId.slice(0, 12)}
          </span>
          <CopyButton text={fullId} label="Copy id" />
        </div>
      </header>

      <Tabs
        tabs={TABS}
        selected={tab}
        ariaLabel="Container sections"
        onSelect={(id) => {
          if (id !== tab) navigate(`/containers/${encodeURIComponent(name)}?tab=${id}`);
        }}
      >
        {(selected) =>
          selected === 'logs' ? (
            <LogConsole
              key={row.id}
              source={{ kind: 'container', id: row.id }}
              services={[]}
              service={undefined}
              onService={() => {}}
              open={logsOpen}
              onOpenChange={setLogsOpen}
              mobile={mobile}
            />
          ) : (
            <div className={styles.cols}>
              <div className={styles.card}>
                {detailError && (
                  <Banner kind={detail ? 'warning' : 'danger'} title={detail ? 'Showing the last loaded details' : 'Could not load the details'}>
                    {detailError}
                  </Banner>
                )}
                {detail ? <DetailFacts detail={detail} /> : !detailError && <Spinner />}
              </div>
              <section className={styles.card} aria-label="Resources · now">
                <PanelLabel as="h2">Resources · now</PanelLabel>
                <dl className={styles.dl}>
                  <dt>CPU</dt>
                  <dd className={styles.mono}>{row.cpu !== null ? formatPercent(row.cpu) : '—'}</dd>
                  <dt>Memory</dt>
                  <dd className={styles.mono}>{memoryText(row.mem, detail?.memoryLimit ?? null)}</dd>
                </dl>
              </section>
            </div>
          )
        }
      </Tabs>
      <ContainerConfirmDialog confirming={actions.confirming} onConfirm={actions.confirm} onCancel={actions.cancel} />
    </section>
  );
}

function DetailFacts({ detail: d }: { detail: ContainerDetail }) {
  const networks = Object.entries(d.networks);
  const labelCount = Object.keys(d.labels).length;
  return (
    <>
      <dl className={styles.dl}>
        <dt>State</dt>
        <dd>
          {d.state} · {d.status}
        </dd>
        <dt>Started</dt>
        <dd>{unknownTime(d.startedAt) ? '—' : <RelativeTime iso={d.startedAt!} />}</dd>
        <dt>Finished</dt>
        <dd>
          {unknownTime(d.finishedAt) ? '—' : <RelativeTime iso={d.finishedAt!} />}{' '}
          {d.exitCode !== null && <span className={styles.mono}>exit {d.exitCode}</span>}
        </dd>
        <dt>Restart policy</dt>
        <dd>{d.restartPolicy || '—'}</dd>
        <dt>Restarts 24h</dt>
        <dd className={styles.mono}>{d.restarts24h}</dd>
        <dt>Ports</dt>
        <dd>
          {d.ports.length === 0
            ? '—'
            : d.ports.map((p) => (
                <span key={p} className={`${styles.chip} ${styles.mono}`}>
                  {p}
                </span>
              ))}
        </dd>
        <dt>Networks</dt>
        <dd>
          {networks.length === 0
            ? '—'
            : networks.map(([net, ip]) => (
                <div key={net} className={styles.mono}>
                  {net} → {ip || '—'}
                </div>
              ))}
        </dd>
        <dt>Mounts</dt>
        <dd>
          {d.mounts.length === 0
            ? '—'
            : d.mounts.map((m) => (
                <div key={m.destination} className={styles.mono}>
                  {m.type}{' '}
                  {m.type === 'volume' && m.name ? <Link to={`/storage/volumes/${encodeURIComponent(m.name)}`}>{m.name}</Link> : m.source} → {m.destination}
                  {m.readOnly ? ' (ro)' : ''}
                </div>
              ))}
        </dd>
      </dl>
      {labelCount > 0 && (
        <details className={styles.labels}>
          <summary>Labels ({labelCount})</summary>
          <dl className={styles.dl}>
            {composeFirst(d.labels).map(([k, v]) => (
              <LabelEntry key={k} labelKey={k} value={v} />
            ))}
          </dl>
        </details>
      )}
    </>
  );
}

function LabelEntry({ labelKey, value }: { labelKey: string; value: string }) {
  return (
    <>
      <dt className={styles.mono}>{labelKey}</dt>
      <dd className={styles.mono}>{value}</dd>
    </>
  );
}
