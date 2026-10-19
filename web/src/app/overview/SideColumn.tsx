import type { DockerEvent, MetricsSummary } from '$lib/api/types';
import type { SSEStatus } from '$lib/sse/client';
import { ActivityFeed } from './ActivityFeed';
import styles from './SideColumn.module.css';
import { TopConsumers } from './TopConsumers';

export interface SideColumnProps {
  events: DockerEvent[];
  connection: SSEStatus;
  top: MetricsSummary['top'] | undefined;
  stackNames: Record<string, string>;
}

/** Activity feed over Top CPU; follows the stacks card in one column under 760px. */
export default function SideColumn({ events, connection, top, stackNames }: SideColumnProps) {
  return (
    <div className={styles.column}>
      <ActivityFeed events={events} connection={connection} />
      <TopConsumers top={top} stackNames={stackNames} />
    </div>
  );
}
