import { TIMELINE_AXIS, TIMELINE_LEGEND, type TimelineLane } from '$lib/stacks/timeline';
import { PanelLabel } from '../components/PanelLabel';
import styles from './HealthTimeline.module.css';

export interface HealthTimelineProps {
  lanes: TimelineLane[];
  selected: string | undefined;
  onSelect: (service: string) => void;
}

const LABEL_ID = 'health-timeline-label';

function restartsText(restarts: number): string {
  return restarts === 1 ? 'restarted once' : `restarted ${restarts} times`;
}

/** "db: healthy 55 min, unhealthy 5 min, restarted 3 times": the lane's runs and restarts as text for the bar image. */
function laneSummary(lane: TimelineLane): string {
  const runs = lane.segments.map((s) => `${s.state === 'nodata' ? 'no data' : s.state} ${Math.max(1, Math.round(s.weight / 60))} min`);
  if (lane.restarts > 0) runs.push(restartsText(lane.restarts));
  return `${lane.service}: ${runs.join(', ')}`;
}

export function HealthTimeline({ lanes, selected, onSelect }: HealthTimelineProps) {
  return (
    <section className={styles.card} aria-labelledby={LABEL_ID}>
      <header className={styles.header}>
        <PanelLabel id={LABEL_ID}>Health · last 60 min</PanelLabel>
        <ul className={styles.legend}>
          {TIMELINE_LEGEND.map(({ tone, label }) => (
            <li key={tone}>
              <span className={styles.swatch} data-tone={tone} aria-hidden="true" />
              {label}
            </li>
          ))}
        </ul>
      </header>
      <div className={styles.grid}>
        {lanes.map((lane) => (
          <LaneRow key={lane.service} lane={lane} selected={lane.service === selected} onSelect={onSelect} />
        ))}
        <div className={styles.axis} aria-hidden="true">
          {TIMELINE_AXIS.map((tick) => (
            <span key={tick}>{tick}</span>
          ))}
        </div>
      </div>
    </section>
  );
}

function LaneRow({ lane, selected, onSelect }: { lane: TimelineLane; selected: boolean; onSelect: (service: string) => void }) {
  return (
    <>
      <button
        type="button"
        className={styles.name}
        aria-pressed={selected}
        data-selected={selected}
        onClick={() => onSelect(lane.service)}
      >
        <span className={styles.service}>{lane.service}</span>
        {lane.restarts > 0 && (
          <>
            <span className={styles.restarts} title={`restarted ${lane.restarts} times in the last 60 min`} aria-hidden="true">
              ↻ {lane.restarts}
            </span>
            <span className={styles.visuallyHidden}>, {restartsText(lane.restarts)}</span>
          </>
        )}
      </button>
      <div className={styles.bar} role="img" aria-label={laneSummary(lane)}>
        {lane.segments.map((segment) => (
          <span
            key={segment.from}
            style={{ flex: segment.weight }}
            title={segment.title}
            data-tone={segment.tone}
            data-pulse={segment.pulse ? 'true' : undefined}
          />
        ))}
      </div>
    </>
  );
}
