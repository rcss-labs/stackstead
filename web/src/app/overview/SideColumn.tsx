import type { DockerEvent, MetricsSummary } from '$lib/api/types';
import type { SSEStatus } from '$lib/sse/client';
import { ActivityFeed, type ActivityFeedProps } from './ActivityFeed';
import styles from './SideColumn.module.css';
import { TopConsumers } from './TopConsumers';

export interface SideColumnProps {
  events: DockerEvent[];
  connection: SSEStatus;
  top: MetricsSummary['top'] | undefined;
  stackNames: Record<string, string>;
  /** Per-event server tag; set on the All stacks screen. */
  serverOf?: ActivityFeedProps['serverOf'];
  /** Top consumers are per host, so the combined view leaves them out. */
  hideTop?: boolean;
}

/** Activity feed over Top CPU; follows the stacks card in one column under 760px. */
export default function SideColumn({ events, connection, top, stackNames, serverOf, hideTop }: SideColumnProps) {
  return (
    <div className={styles.column}>
      <ActivityFeed events={events} connection={connection} serverOf={serverOf} />
      {!hideTop && <TopConsumers top={top} stackNames={stackNames} />}
    </div>
  );
}
