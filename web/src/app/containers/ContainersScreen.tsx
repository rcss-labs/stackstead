import { Fragment, useEffect, useRef, useState } from 'react';
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
import { ContainerPeek } from './ContainerPeek';
import { ContainerRow } from './ContainerRow';
import styles from './ContainersScreen.module.css';
import { useContainerAction } from './useContainerAction';
import { useContainerKeys } from './useContainerKeys';
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
  // Focus follows the container, not its position, so a live re-sort never moves the ring to another container.
  const [focusId, setFocusId] = useState<string | undefined>();
  const [fallbackIndex, setFallbackIndex] = useState(0);
  // Container name whose log peek is open; names survive a container being recreated under a new id.
  const [openPeek, setOpenPeek] = useState<string | undefined>();
  const rowRefs = useRef(new Map<string, HTMLDivElement>());
  const mobile = useIsMobile();

  const list = useContainerList();
  const actions = useContainerAction((_row, fresh) => {
    if (fresh) list.patch(fresh);
    list.refresh();
  });

  const { rows, error } = list;
  const all = rows ?? [];
  const counts = chipCounts(all);
  const visible = filterContainers(sortContainers(all), chip, query);
  // When the focused container left the list, clamp the last index so the roving tab stop never sits on a gone row.
  const idIndex = focusId === undefined ? -1 : visible.findIndex((c) => c.id === focusId);
  const focused = idIndex >= 0 ? idIndex : Math.min(fallbackIndex, Math.max(visible.length - 1, 0));
  function focusRow(index: number) {
    setFocusId(visible[index]?.id);
    setFallbackIndex(index);
  }
  const { confirming, confirm, cancel } = actions;

  // Keep state honest about the list: the remembered position tracks the focused row, and no peek stays open for a container that is no longer shown.
  useEffect(() => {
    if (fallbackIndex !== focused) setFallbackIndex(focused);
  }, [fallbackIndex, focused]);
  const peekIsVisible = openPeek === undefined || visible.some((c) => c.name === openPeek);
  useEffect(() => {
    if (!peekIsVisible) setOpenPeek(undefined);
  }, [peekIsVisible]);

  function togglePeek(index: number) {
    focusRow(index);
    setOpenPeek((cur) => (cur === visible[index]?.name ? undefined : visible[index]?.name));
  }

  useContainerKeys({
    enabled: rows !== undefined,
    count: visible.length,
    focusIndex: focused,
    onMove: (index) => {
      focusRow(index);
      const el = rowRefs.current.get(visible[index].id);
      el?.focus();
      el?.scrollIntoView?.({ block: 'nearest' });
    },
    onToggle: () => visible.length > 0 && togglePeek(focused),
    onRestart: () => visible.length > 0 && actions.request(visible[focused], 'restart'),
    onClose: () => {
      if (openPeek === undefined) return;
      setOpenPeek(undefined);
      // The log region that held focus unmounts with the peek; hand focus back to the row instead of body.
      rowRefs.current.get(visible[focused]?.id)?.focus();
    },
  });

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
              <Chip key={id} pressed={chip === id} count={counts[id]} tone={id === 'problems' ? 'danger' : undefined} onClick={() => {
                  setChip(id);
                  setFocusId(undefined);
                  setFallbackIndex(0);
                }}>
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
                  <Fragment key={c.id}>
                    <ContainerRow
                      row={c}
                      index={i}
                      focused={i === focused}
                      open={openPeek === c.name}
                      peekId={`peek-${c.id}`}
                      mobile={mobile}
                      onFocus={focusRow}
                      onToggle={() => togglePeek(i)}
                      rowRef={(el) => {
                        if (el) rowRefs.current.set(c.id, el);
                        else rowRefs.current.delete(c.id);
                      }}
                    />
                    {openPeek === c.name && (
                      <ContainerPeek
                        row={c}
                        id={`peek-${c.id}`}
                        mobile={mobile}
                        reason={actions.reasonFor(c)}
                        onRequest={(action) => actions.request(c, action)}
                      />
                    )}
                  </Fragment>
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
