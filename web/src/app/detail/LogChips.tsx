import { seriesVar } from './logConsoleShared';
import styles from './LogLines.module.css';

export interface LogChipsProps {
  services: string[];
  selected: string | undefined;
  onSelect: (service: string | undefined) => void;
  colorIndex: (service: string) => number;
}

export function LogChips({ services, selected, onSelect, colorIndex }: LogChipsProps) {
  if (services.length === 0) return null;
  return (
    <div className={styles.chips} aria-label="Service filter" role="group">
      <button type="button" className={styles.chip} aria-label="All services" aria-pressed={selected === undefined} onClick={() => onSelect(undefined)}>
        <i className={styles.swatch} style={{ background: 'var(--muted)' }} />
        all
      </button>
      {services.map((service) => (
        <button key={service} type="button" className={styles.chip} aria-pressed={selected === service} onClick={() => onSelect(service)}>
          <i className={styles.swatch} style={{ background: seriesVar(colorIndex(service)) }} />
          {service}
        </button>
      ))}
    </div>
  );
}
