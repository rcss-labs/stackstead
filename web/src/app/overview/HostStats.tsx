import { Link } from 'react-router';
import type { HostSnapshot } from '$lib/api/types';
import { hostStats } from '$lib/dashboard/tiles';
import type { HostStat } from '$lib/dashboard/tiles';
import type { HostSample } from '$lib/stores/host';
import { PanelLabel } from '../components/PanelLabel';
import { Sparkline } from '../components/Sparkline';
import styles from './HostStats.module.css';

export interface HostStatsProps {
  host: HostSnapshot | undefined;
  history: HostSample[];
}

/** The strip of host figures under the towers; nothing until the first snapshot arrives. */
export function HostStats({ host, history }: HostStatsProps) {
  if (!host) return null;
  const stats = hostStats(host, history, new Date());
  if (stats.length === 0) return null;
  return (
    <div className={styles.strip}>
      {stats.map((stat) => (
        <StatCell key={stat.id} stat={stat} />
      ))}
    </div>
  );
}

function StatCell({ stat }: { stat: HostStat }) {
  const body = (
    <>
      <div className={styles.top}>
        <PanelLabel as="span">{stat.label}</PanelLabel>
        {stat.aux && (
          <span className={styles.aux} data-tone={stat.auxTone}>
            {stat.aux}
          </span>
        )}
      </div>
      <div className={styles.middle}>
        <span className={styles.value}>{stat.value}</span>
        <span className={styles.spark}>
          <Sparkline values={stat.series} tone={stat.tone} width={54} height={18} />
        </span>
      </div>
      {stat.sub && <div className={styles.sub}>{stat.sub}</div>}
    </>
  );
  return stat.href ? (
    <Link to={stat.href} className={styles.cell} data-stat={stat.id}>
      {body}
    </Link>
  ) : (
    <div className={styles.cell} data-stat={stat.id}>
      {body}
    </div>
  );
}
