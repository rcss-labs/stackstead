import { useNavigate } from 'react-router';
import type { StackSummary } from '$lib/api/types';
import { towerBlocks, towerLabelTone, towerTitle } from '$lib/dashboard/overview';
import styles from './StackTowers.module.css';

import { useServer } from '../servers/ServerContext';
/** One column of blocks per stack, one block per service, bottom-up. */
export interface StackTowersProps {
  items: StackSummary[];
  /** Where a tower leads; defaults to the stack on the viewed server. */
  hrefOf?: (stack: StackSummary) => string;
  /** React key; defaults to the stack name, which is only unique within one server. */
  keyOf?: (stack: StackSummary) => string;
}

export function StackTowers({ items, hrefOf, keyOf }: StackTowersProps) {
  const navigate = useNavigate();
  const { href } = useServer();
  if (items.length === 0) return null;
  const sorted = [...items].sort((a, b) => a.name.localeCompare(b.name));

  return (
    <div className={styles.scroller}>
      <div className={styles.row}>
        {sorted.map((s) => (
          <button key={keyOf ? keyOf(s) : s.name} type="button" className={styles.tower} title={towerTitle(s)} onClick={() => navigate(hrefOf ? hrefOf(s) : href(`/stacks/${encodeURIComponent(s.name)}`))}>
            <span className={styles.blocks} aria-hidden="true">
              {towerBlocks(s).map((block, i) => (
                <span key={i} className={styles.block} data-block={block} />
              ))}
            </span>
            <span className={styles.name} data-tone={towerLabelTone(s)}>
              {s.name}
            </span>
          </button>
        ))}
      </div>
    </div>
  );
}
