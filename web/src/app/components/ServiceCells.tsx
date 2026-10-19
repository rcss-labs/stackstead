import styles from './ServiceCells.module.css';

export interface ServiceCellsProps {
  count: number;
  good: number;
  bad: number;
  starting: number;
  title: string;
}

type CellTone = 'success' | 'warning' | 'danger' | 'off';

export function ServiceCells({ count, good, bad, starting, title }: ServiceCellsProps) {
  // Cells are ordered good, starting, bad, then off (stopped / not running).
  const tones: CellTone[] = Array.from({ length: count }, (_, i) => {
    if (i < good) return 'success';
    if (i < good + starting) return 'warning';
    if (i < good + starting + bad) return 'danger';
    return 'off';
  });
  return (
    <span className={styles.cells} aria-hidden="true" title={title}>
      {tones.map((tone, i) => (
        <span key={i} className={styles.cell} data-tone={tone} />
      ))}
    </span>
  );
}
