import type { Network, NetworkContainer } from '$lib/api/types';
import { connectDisabledReason, containerKey, containerStatus, removeDisabledReason } from '$lib/networks/networks';
import { Button } from '../components/Button';
import { CopyButton } from '../components/CopyButton';
import { EmptyState } from '../components/EmptyState';
import { PanelLabel } from '../components/PanelLabel';
import { StatusBadge } from '../components/StatusBadge';
import { Tag } from '../components/Tag';
import styles from './NetworkDetail.module.css';
import { NetworkTopology } from './NetworkTopology';

export interface NetworkDetailProps {
  network: Network;
  highlighted: string | null;
  /** A remove, detach or connect for this network is in flight. */
  busy: boolean;
  onToggle: (container: NetworkContainer) => void;
  onInspect: () => void;
  onConnect: () => void;
  onRemove: () => void;
  onDetach: (container: NetworkContainer) => void;
}

const yesNo = (value: boolean) => (value ? 'yes' : 'no');

function facts(n: Network): [string, string][] {
  return [
    ['Driver', n.driver],
    ['Scope', n.scope],
    ['Subnet', n.subnets[0] ?? ''],
    ['Gateway', n.gateways[0] ?? ''],
    ['Internal', yesNo(n.internal)],
    ['Attachable', yesNo(n.attachable)],
    ['IPv6', n.ipv6 ? 'enabled' : 'disabled'],
    ['Created', /^\d{4}-\d{2}-\d{2}/.test(n.created) ? n.created.slice(0, 10) : ''],
  ];
}

export function NetworkDetail({ network, highlighted, busy, onToggle, onInspect, onConnect, onRemove, onDetach }: NetworkDetailProps) {
  const { containers } = network;
  const fixed = connectDisabledReason(network);
  const detachReason = fixed && `Containers can't leave the ${network.name} network at runtime`;
  return (
    <div className={styles.inner}>
      <header className={styles.header}>
        <div className={styles.identity}>
          <div className={styles.titleLine}>
            <h2 id="network-detail-heading" tabIndex={-1}>
              {network.name}
            </h2>
            <Tag>{network.driver}</Tag>
            {network.internal && <Tag tone="warning">internal</Tag>}
          </div>
          <div className={styles.idLine}>
            <span>{(network.fullId || network.id).slice(0, 12)}</span>
            <CopyButton variant="icon" size="xs" text={network.fullId || network.id} label="Copy network ID" />
          </div>
        </div>
        <div className={styles.actions}>
          <Button variant="ghost" size="sm" onClick={onInspect}>
            Inspect
          </Button>
          <Button variant="secondary" size="sm" disabledReason={fixed ?? (busy ? 'Another change is in progress' : undefined)} onClick={onConnect}>
            Connect container
          </Button>
          <Button variant="danger" size="sm" disabledReason={removeDisabledReason(network) ?? (busy ? 'Another change is in progress' : undefined)} onClick={onRemove}>
            Remove
          </Button>
        </div>
      </header>

      <dl className={`${styles.card} ${styles.facts}`} aria-label="Configuration">
        {facts(network).map(([key, value]) => (
          <div key={key} className={styles.fact}>
            <dt>
              <PanelLabel as="span">{key}</PanelLabel>
            </dt>
            <dd>{value || '—'}</dd>
          </div>
        ))}
      </dl>

      <section className={styles.section} aria-labelledby="network-topology-heading">
        <div className={styles.sectionHead}>
          <PanelLabel as="h2" id="network-topology-heading">
            Topology
          </PanelLabel>
          <span className={styles.hint}>Click a container to highlight it</span>
        </div>
        <NetworkTopology network={network} highlighted={highlighted} onToggle={onToggle} />
      </section>

      <section className={styles.section} aria-labelledby="network-containers-heading">
        <div className={styles.sectionHead}>
          <PanelLabel as="h2" id="network-containers-heading">
            Connected containers
          </PanelLabel>
          <span className={styles.sectionCount}>{containers.length}</span>
        </div>
        {containers.length === 0 ? (
          <EmptyState
            title="No containers on this network"
            description={network.builtin ? 'Containers started with this network mode appear here.' : 'This network is unused and can be removed.'}
          />
        ) : (
          <div className={`${styles.card} ${styles.tableScroll}`}>
            <div className={styles.table} role="table" aria-label="Connected containers">
              <div role="rowgroup">
                <div className={`${styles.tr} ${styles.head}`} role="row">
                  {['Container', 'Stack', 'IPv4', 'MAC', 'Status'].map((label) => (
                    <div key={label} role="columnheader">
                      <PanelLabel as="span">{label}</PanelLabel>
                    </div>
                  ))}
                  <div role="columnheader" aria-label="Actions" />
                </div>
              </div>
              <div role="rowgroup" className={styles.body}>
                {containers.map((c) => {
                  const key = containerKey(c);
                  const selected = highlighted === key;
                  return (
                    <div key={key} className={styles.tr} role="row" aria-selected={selected} onClick={() => onToggle(c)}>
                      <div role="cell" className={`${styles.cell} ${styles.colName}`}>
                        {/* The row toggles on click; this button gives keyboard users the same toggle. */}
                        <button
                          type="button"
                          className={styles.cellName}
                          aria-pressed={selected}
                          onClick={(e) => {
                            e.stopPropagation();
                            onToggle(c);
                          }}
                        >
                          {c.name}
                        </button>
                      </div>
                      <div role="cell" className={`${styles.cell} ${styles.stack} ${styles.colStack}`}>
                        {c.stack || '—'}
                      </div>
                      <div role="cell" className={`${styles.cell} ${styles.mono} ${styles.colIp}`}>
                        {c.ipv4 || '—'}
                      </div>
                      <div role="cell" className={`${styles.cell} ${styles.mono} ${styles.muted} ${styles.colMac}`}>
                        {c.mac || '—'}
                      </div>
                      <div role="cell" className={styles.colStatus}>
                        <StatusBadge status={containerStatus(c)} />
                      </div>
                      <div role="cell" className={`${styles.end} ${styles.colAction}`} onClick={(e) => e.stopPropagation()}>
                        <Button
                          variant="ghost"
                          size="xs"
                          aria-label={`Detach ${c.name}`}
                          disabledReason={detachReason || (busy ? 'Another change is in progress' : undefined)}
                          onClick={() => onDetach(c)}
                        >
                          Detach
                        </Button>
                      </div>
                    </div>
                  );
                })}
              </div>
            </div>
          </div>
        )}
      </section>
    </div>
  );
}
