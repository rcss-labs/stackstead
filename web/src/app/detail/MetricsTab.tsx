import { useEffect, useState } from 'react';
import { api } from '$lib/api/client';
import type { Range, SeriesSet } from '$lib/metrics/series';
import type { StackLiveMetrics } from '$lib/stacks/liveMetrics';
import { createMetricsController, type MetricsController } from '$lib/stacks/metrics';
import { Banner } from '../components/Banner';
import { EmptyState } from '../components/EmptyState';
import { Segmented } from '../components/Segmented';
import { Spinner } from '../components/Spinner';
import { useStore } from '../hooks/useStore';
import { MetricsChart, type CreatePlot } from '../stacks/MetricsChart';
import styles from './MetricsTab.module.css';

export interface MetricsTabProps {
  stackName: string;
  service: string;
  /** The page's shared live source. */
  live: StackLiveMetrics;
  metricsDegraded: boolean;
  metricsIntervalSeconds?: number;
  metricsRetentionSeconds?: number;
  allServices: string[];
  createPlot?: CreatePlot;
}

const RANGES: Range[] = ['1h', '6h', '24h', '72h'];
const RANGE_OPTIONS = RANGES.map((r) => ({ value: r, label: r }));

/** One controller per mount (the pane keys this by service); created in an effect so StrictMode replays stay clean. */
export function MetricsTab(props: MetricsTabProps) {
  const { stackName, live } = props;
  const [ctl, setCtl] = useState<MetricsController | undefined>();
  useEffect(() => {
    const created = createMetricsController(stackName, { history: (q, signal) => api.metricsHistory(stackName, q, signal), live });
    setCtl(created);
    void created.reload();
    return () => created.close();
  }, [stackName, live]);
  return ctl ? <MetricsBody key={props.service} ctl={ctl} {...props} /> : <Spinner label="Loading metrics" />;
}

function MetricsBody({ ctl, service, metricsDegraded, metricsIntervalSeconds, metricsRetentionSeconds, allServices, createPlot }: MetricsTabProps & { ctl: MetricsController }) {
  const all = useStore(ctl.series);
  const range = useStore(ctl.range);
  const status = useStore(ctl.status);
  const degraded = useStore(ctl.degraded);
  const loaded = useStore(ctl.loaded);
  // The spinner is for the first load only; a range change keeps the old charts until the new data lands.
  const [everLoaded, setEverLoaded] = useState(false);
  if (loaded && !everLoaded) setEverLoaded(true);

  const points = all.get(service);
  const data: SeriesSet = new Map(points ? [[service, points]] : []);
  const lastLimit = points && points.length > 0 ? points[points.length - 1]!.memLimit : 0;

  const chart = (title: string, series: { label: string; metric: 'cpu' | 'mem' | 'netRx' | 'netTx' | 'blkRead' | 'blkWrite' }[], unit: 'percent' | 'bytes' | 'bytesPerSec', memLimit?: number) => (
    <MetricsChart
      title={`${title} · ${range}`}
      series={series.map((s) => ({ ...s, service }))}
      data={data}
      unit={unit}
      allServices={allServices}
      memLimit={memLimit}
      createPlot={createPlot}
    />
  );

  return (
    <section className={styles.panel} aria-label="Resource metrics">
      {metricsDegraded && <Banner kind="warning" title="Metrics are not being recorded (database write failures); stack management is unaffected" />}
      {degraded && (
        <Banner kind="danger" title="Could not load metric history">
          Live samples may still appear below.
        </Banner>
      )}
      <div className={styles.toolbar}>
        <Segmented label="Range" size="sm" options={RANGE_OPTIONS} value={range} onChange={(r) => ctl.range.set(r as Range)} />
        <span className={styles.spacer} />
        {metricsIntervalSeconds !== undefined && metricsRetentionSeconds !== undefined && (
          <span className={styles.note}>
            sampled every {metricsIntervalSeconds} s · retention {metricsRetentionSeconds / 3600} h
          </span>
        )}
        <span className={styles.note} role="status">
          Live: {status}
        </span>
      </div>
      {!everLoaded ? (
        <Spinner label="Loading metrics" />
      ) : !points || points.length === 0 ? (
        <EmptyState title="No samples yet" description="The sampler records every 10 s." />
      ) : (
        <div className={styles.grid} aria-busy={loaded ? undefined : 'true'}>
          {chart('CPU', [{ label: service, metric: 'cpu' }], 'percent')}
          {chart('Memory', [{ label: service, metric: 'mem' }], 'bytes', lastLimit > 0 ? lastLimit : undefined)}
          {chart('Network', [{ label: 'RX', metric: 'netRx' }, { label: 'TX', metric: 'netTx' }], 'bytesPerSec')}
          {chart('Disk I/O', [{ label: 'read', metric: 'blkRead' }, { label: 'write', metric: 'blkWrite' }], 'bytesPerSec')}
        </div>
      )}
    </section>
  );
}
