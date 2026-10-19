import { useId, useRef, useState } from 'react';
import { filterContainers, chipCounts, sortContainers, type ContainerChip } from '$lib/containers/list';
import { ICONS } from '$lib/shell/icons';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { Chip } from '../components/Chip';
import { EmptyState } from '../components/EmptyState';
import { PanelLabel } from '../components/PanelLabel';
import { Spinner } from '../components/Spinner';
import { useIsMobile } from '../hooks/useIsMobile';
import { ContainerConfirmDialog } from './ContainerActions';
import { ContainerRow } from './ContainerRow';
import styles from './ContainersScreen.module.css';
import { useContainerAction } from './useContainerAction';
import { useContainerList } from './useContainerList';

const CHIPS: { id: ContainerChip; label: string }[] = [
  { id: 'all', label: 'All' },
  { id: 'running', label: 'Running' },
  { id: 'stopped', label: 'Stopped' },
  { id: 'problems', label: 'Problems' },
  { id: 'unmanaged', label: 'Unmanaged' },
];

export function ContainersScreen() {
  const [chip, setChip] = useState<ContainerChip>('all');
  const [query, setQuery] = useState('');
  const [focusIndex, setFocusIndex] = useState(0);
  // Container name whose log peek is open; the peek itself arrives with the keyboard work.
  const [openPeek, setOpenPeek] = useState<string | undefined>();
  const rowEls = useRef<(HTMLDivElement | null)[]>([]);
  const peekBase = useId();
  const mobile = useIsMobile();

  const list = useContainerList();
  const { confirming, confirm, cancel } = useContainerAction((_row, fresh) => {
    if (fresh) list.patch(fresh);
    list.refresh();
  });

  const { rows, error } = list;
  const all = rows ?? [];
  const counts = chipCounts(all);
  const visible = filterContainers(sortContainers(all), chip, query);
  // Clamp so a shrinking list never leaves the roving tab stop on a row that is gone.
  const focused = Math.min(focusIndex, Math.max(visible.length - 1, 0));

  return (
    <section className={styles.page}>
      <header className={styles.head}>
        <h1 id="containers-heading">Containers</h1>
        {rows && (
          <span className={styles.counts}>
            {counts.all} · {counts.running} running
          </span>
        )}
        {!mobile && (
          <span className={styles.hints}>
            <kbd>j</kbd> <kbd>k</kbd> move · <kbd>↵</kbd> peek logs · <kbd>r</kbd> restart
          </span>
        )}
      </header>

      {all.length > 0 && (
        <div className={styles.filters}>
          <div className={styles.chips}>
            {CHIPS.map(({ id, label }) => (
              <Chip key={id} pressed={chip === id} count={counts[id]} tone={id === 'problems' ? 'danger' : undefined} onClick={() => setChip(id)}>
                {label}
              </Chip>
            ))}
          </div>
          <label className={styles.search}>
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
              <path d={ICONS.search} />
            </svg>
            <input type="search" aria-label="Search containers" placeholder="Filter containers" value={query} onChange={(e) => setQuery(e.target.value)} />
          </label>
        </div>
      )}

      {rows === undefined && error ? (
        <>
          <Banner kind="danger" title="Could not load containers">
            {error}
          </Banner>
          <p>
            <Button variant="secondary" onClick={() => list.refresh()}>
              Retry
            </Button>
          </p>
        </>
      ) : rows === undefined ? (
        <div className={styles.loading}>
          <Spinner label="Loading containers" />
        </div>
      ) : all.length === 0 ? (
        <EmptyState title="No containers on this host." description="Containers appear here as soon as Docker creates them." />
      ) : (
        <div className={styles.card}>
          <div className={styles.scroller}>
            <div role="table" aria-labelledby="containers-heading">
              <div role="rowgroup" className={styles.headGroup} data-desktop>
                <div role="row" className={styles.headRow}>
                  <div role="columnheader" />
                  {['Container', 'Stack', 'Image', 'State', 'Ports', 'CPU', 'Memory'].map((label) => (
                    <div key={label} role="columnheader">
                      <PanelLabel as="div">{label}</PanelLabel>
                    </div>
                  ))}
                  <div role="columnheader" aria-label="Restarts 24h">
                    <PanelLabel as="div">↻ 24h</PanelLabel>
                  </div>
                  <div role="columnheader" aria-label="Peek logs" />
                </div>
              </div>
              <div role="rowgroup">
                {visible.map((c, i) => (
                  <ContainerRow
                    key={c.id}
                    row={c}
                    index={i}
                    focused={i === focused}
                    open={openPeek === c.name}
                    peekId={`${peekBase}-peek-${c.id}`}
                    mobile={mobile}
                    onFocus={setFocusIndex}
                    onToggle={() => setOpenPeek((cur) => (cur === c.name ? undefined : c.name))}
                    rowRef={(el) => {
                      rowEls.current[i] = el;
                    }}
                  />
                ))}
              </div>
            </div>
          </div>
          {visible.length === 0 && <p className={styles.none}>No containers match the current filter.</p>}
        </div>
      )}

      <ContainerConfirmDialog confirming={confirming} onConfirm={confirm} onCancel={cancel} />
    </section>
  );
}
