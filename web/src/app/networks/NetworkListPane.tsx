import type { Network } from '$lib/api/types';
import { filterCounts, filterNetworks, networkHealth, type NetworkFilter } from '$lib/networks/networks';
import { Chip } from '../components/Chip';
import { Led } from '../components/Led';
import { Tag } from '../components/Tag';
import styles from './NetworkListPane.module.css';

const CHIPS: [NetworkFilter, string][] = [
  ['all', 'All'],
  ['active', 'In use'],
  ['internal', 'Internal'],
  ['unused', 'Unused'],
];

export interface NetworkListPaneProps {
  networks: Network[];
  selected: string | undefined;
  filter: NetworkFilter;
  query: string;
  onFilter: (filter: NetworkFilter) => void;
  onQuery: (query: string) => void;
  onSelect: (network: Network) => void;
}

export function NetworkListPane({ networks, selected, filter, query, onFilter, onQuery, onSelect }: NetworkListPaneProps) {
  const counts = filterCounts(networks);
  const visible = filterNetworks(networks, filter, query);
  return (
    <div className={styles.pane}>
      <div className={styles.toolbar}>
        <input
          className={styles.search}
          type="search"
          aria-label="Filter networks"
          placeholder="Filter by name or subnet"
          value={query}
          onChange={(e) => onQuery(e.target.value)}
        />
        <div className={styles.chips} role="group" aria-label="Network filter">
          {CHIPS.map(([value, label]) => (
            <Chip key={value} pressed={filter === value} count={counts[value]} onClick={() => onFilter(value)}>
              {label}
            </Chip>
          ))}
        </div>
      </div>
      {visible.length === 0 ? (
        <p className={styles.none}>No networks match this filter.</p>
      ) : (
        <ul className={styles.list} role="listbox" aria-label="Networks">
          {visible.map((n) => (
            <li
              key={n.id}
              role="option"
              aria-selected={n.name === selected}
              tabIndex={0}
              className={styles.row}
              onClick={() => onSelect(n)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' || e.key === ' ') {
                  e.preventDefault();
                  onSelect(n);
                }
              }}
            >
              <Led size={8} tone={networkHealth(n)} />
              <span className={styles.title}>
                <span className={styles.name}>{n.name}</span>
                {n.internal && <Tag tone="warning">internal</Tag>}
                {n.builtin && <Tag tone="muted">system</Tag>}
              </span>
              <span className={styles.count} aria-label={`${n.containers.length} containers`}>
                {n.containers.length}
              </span>
              <span className={styles.meta}>
                {n.driver} · {n.subnets[0] || 'no subnet'}
              </span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
