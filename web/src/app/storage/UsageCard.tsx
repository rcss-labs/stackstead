import type { CSSProperties } from 'react';
import type { StorageOverview, StoragePlanRequest } from '$lib/api/types';
import { sizeText } from '$lib/storage/storage';
import { cleanupRequest, KINDS, usageSegments, type StorageKind } from '$lib/storage/usage';
import { Button } from '../components/Button';
import { PanelLabel } from '../components/PanelLabel';
import styles from './UsageCard.module.css';

export interface UsageCardProps {
  overview: StorageOverview;
  selected: StorageKind;
  dockerDown: boolean;
  /** `Clean up dangling images (<n> · <range>)`, computed by the screen from the loaded image list. */
  danglingLabel: string;
  /** Set when the image list failed to load, so the dangling cleanup cannot be sized or trusted. */
  danglingUnavailable?: string;
  onSelect: (kind: StorageKind) => void;
  onCleanup: (request: StoragePlanRequest) => void;
}

const CLEANUP_LABELS: Record<StorageKind, string | undefined> = {
  images: undefined, // uses danglingLabel
  volumes: 'Clean up unused volumes',
  'build-cache': 'Clean up build cache',
  layers: undefined,
};

const kindStyle = (color: string) => ({ '--kind': color }) as CSSProperties;

export function UsageCard({ overview, selected, dockerDown, danglingLabel, danglingUnavailable, onSelect, onCleanup }: UsageCardProps) {
  const segments = usageSegments(overview);
  return (
    <div className={styles.card}>
      <div className={styles.bar} role="group" aria-label="Usage by kind">
        {segments.map((segment) => {
          const info = KINDS.find((k) => k.id === segment.kind)!;
          const totals = info.totals(overview);
          return (
            <button
              key={segment.kind}
              type="button"
              className={styles.segment}
              data-kind={segment.kind}
              aria-pressed={segment.kind === selected}
              // An unknown size gets a placeholder share, which is not worth announcing.
              aria-label={`${info.label} · ${sizeText(segment.bytes, totals.refreshing)}${segment.bytes === null ? '' : ` · ${segment.percent}%`}`}
              // A zero share keeps a 4px sliver so the kind stays reachable.
              style={{ ...kindStyle(info.color), flex: segment.share > 0 ? segment.share : '0 0 4px' }}
              onClick={() => onSelect(segment.kind)}
            />
          );
        })}
      </div>
      <div className={styles.tiles} role="group" aria-label="Storage kinds">
        {KINDS.map((info) => {
          const totals = info.totals(overview);
          const request = cleanupRequest(info.id);
          const cleanupLabel = info.id === 'images' ? danglingLabel : CLEANUP_LABELS[info.id];
          return (
            <div key={info.id} className={styles.tile} data-selected={info.id === selected ? 'true' : undefined}>
              <button type="button" className={styles.pick} style={kindStyle(info.color)} aria-pressed={info.id === selected} onClick={() => onSelect(info.id)}>
                <PanelLabel as="span">
                  <i className={styles.swatch} aria-hidden="true" /> {info.label}
                </PanelLabel>
                <span className={styles.size}>{sizeText(totals.size, totals.refreshing)}</span>
                <span className={styles.meta}>
                  {totals.count} · {sizeText(totals.reclaimable, totals.refreshing)} reclaimable
                </span>
              </button>
              {request && cleanupLabel && (
                <Button variant="danger" size="sm" disabledReason={dockerDown ? 'Docker is unavailable' : info.id === 'images' ? danglingUnavailable : undefined} onClick={() => onCleanup(request)}>
                  {cleanupLabel}
                </Button>
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
}
