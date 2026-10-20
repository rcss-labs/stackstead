import { useId, useState } from 'react';
import { Link } from 'react-router';
import type { Network } from '$lib/api/types';
import { isLanDriver, networkOwner, removeDisabledReason } from '$lib/networks/networks';
import { Button } from '../components/Button';
import { Tag } from '../components/Tag';
import styles from './NetworkCard.module.css';

export interface NetworkCardProps {
  network: Network;
  removing: boolean;
  /** Some network is being removed; every Remove waits for it. */
  busy: boolean;
  onRemove: () => void;
}

function Owner({ network }: { network: Network }) {
  const owner = networkOwner(network);
  if (owner.kind === 'stack') return <Link to={`/stacks/${encodeURIComponent(owner.stack)}`}>stack {owner.stack}</Link>;
  if (owner.kind === 'compose') return <>compose {owner.project}</>;
  return <>{owner.kind === 'builtin' ? 'built-in' : 'unmanaged'}</>;
}

export function NetworkCard({ network, removing, busy, onRemove }: NetworkCardProps) {
  const uid = useId();
  const nameId = `${uid}-name`;
  const listId = `${uid}-containers`;
  const [expanded, setExpanded] = useState(false);
  const { subnets, gateways, parent, containers } = network;
  // Built-in networks are noisy (the default bridge), so their containers fold away and empty details are hidden.
  const compact = network.builtin;
  const showDetails = !compact || !!(subnets[0] || gateways[0] || parent);

  const list = (
    <div id={listId} className={styles.list}>
      {containers.map((container) => (
        <div key={container.name} className={styles.endpoint}>
          <Link className={styles.endpointName} to={`/containers/${encodeURIComponent(container.name)}`}>
            {container.name}
          </Link>
          <span className={styles.ip}>{container.ipv4 || '—'}</span>
        </div>
      ))}
    </div>
  );
  const label = `Show containers on ${network.name}`;

  return (
    <article className={styles.card} aria-labelledby={nameId} data-network-id={network.id}>
      <div className={styles.top}>
        <h2 id={nameId} className={styles.name} tabIndex={-1}>
          {network.name}
        </h2>
        <Tag tone={isLanDriver(network.driver) ? 'info' : 'muted'}>{network.driver}</Tag>
        <span className={styles.owner}>
          <Owner network={network} />
        </span>
      </div>
      {showDetails && (
        <dl className={styles.dl}>
          <dt>Subnet</dt>
          <dd>
            <span>{subnets[0] || '—'}</span>
            {subnets.length > 1 && (
              <span className={styles.more} title={subnets.join(', ')}>
                +{subnets.length - 1}
              </span>
            )}
          </dd>
          <dt>Gateway</dt>
          <dd>{gateways[0] || '—'}</dd>
          <dt>Parent</dt>
          <dd>{parent || '—'}</dd>
        </dl>
      )}
      <div className={styles.containers}>
        {containers.length === 0 ? (
          <span className={styles.none}>No containers attached</span>
        ) : compact ? (
          <>
            <button type="button" className={styles.expand} aria-label={label} title={label} aria-expanded={expanded} aria-controls={listId} onClick={() => setExpanded((v) => !v)}>
              <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" aria-hidden="true">
                <path d="M6 9l6 6 6-6" />
              </svg>
              {containers.length} container{containers.length === 1 ? '' : 's'}
            </button>
            {expanded && list}
          </>
        ) : (
          list
        )}
      </div>
      <div className={styles.actions}>
        <Button variant="danger" size="sm" disabledReason={removeDisabledReason(network) ?? (busy ? 'Removing network' : undefined)} loading={removing} onClick={onRemove}>
          Remove
        </Button>
      </div>
    </article>
  );
}
