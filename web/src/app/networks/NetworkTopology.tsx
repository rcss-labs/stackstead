import type { Network, NetworkContainer } from '$lib/api/types';
import { containerKey, containerStatus, groupByStack, STATUS_TONE, upstream } from '$lib/networks/networks';
import { Led } from '../components/Led';
import { PanelLabel } from '../components/PanelLabel';
import styles from './NetworkTopology.module.css';

export interface NetworkTopologyProps {
  network: Network;
  highlighted: string | null;
  onToggle: (container: NetworkContainer) => void;
}

export function NetworkTopology({ network, highlighted, onToggle }: NetworkTopologyProps) {
  const route = upstream(network);
  const groups = groupByStack(network.containers);
  const subnet = network.subnets[0];
  const gateway = network.gateways[0];
  const warning = route.warning ? 'true' : undefined;
  return (
    <div className={styles.card}>
      <div className={styles.diagram}>
        <div className={styles.pill} data-warning={warning}>
          {route.label}
        </div>
        <div className={styles.dash} data-warning={warning} />
        <div className={styles.node}>
          <div className={styles.nodeName}>{network.name}</div>
          <div className={styles.nodeSub}>{gateway ? `${subnet || '—'} · gw ${gateway}` : network.driver}</div>
        </div>
        {groups.length === 0 ? (
          <>
            <div className={styles.dash} />
            <div className={styles.pill}>No containers attached</div>
          </>
        ) : (
          <>
            <div className={styles.solid} />
            <div className={styles.bus}>
              {groups.map((group, i) => (
                <div key={group.stack} className={styles.column} data-first={i === 0} data-last={i === groups.length - 1}>
                  <div className={styles.rail} />
                  <div className={styles.drop} />
                  <div className={styles.group} role="group" aria-label={group.stack || 'standalone'}>
                    <PanelLabel tone="faint">{group.stack || 'standalone'}</PanelLabel>
                    {group.containers.map((c) => {
                      const key = containerKey(c);
                      return (
                        <button key={key} type="button" className={styles.container} aria-pressed={highlighted === key} onClick={() => onToggle(c)}>
                          <Led size={7} tone={STATUS_TONE[containerStatus(c)]} />
                          <span className={styles.containerName}>{c.name}</span>
                          <span className={styles.ip}>{c.ipv4 || '—'}</span>
                        </button>
                      );
                    })}
                  </div>
                </div>
              ))}
            </div>
          </>
        )}
      </div>
    </div>
  );
}
