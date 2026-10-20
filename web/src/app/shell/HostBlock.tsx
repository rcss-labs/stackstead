import { formatDuration } from '$lib/format';
import { useStore } from '../hooks/useStore';
import { Led } from '../components/Led';
import { useServer } from '../servers/ServerContext';
import styles from './HostBlock.module.css';

const GIB = 1024 ** 3;

function tone(percent: number): 'accent' | 'warning' | 'danger' {
  if (percent >= 90) return 'danger';
  if (percent >= 75) return 'warning';
  return 'accent';
}

function uptimeText(seconds: number): string {
  const text = formatDuration(seconds * 1000);
  if (seconds >= 86400) return `${text.split('d')[0]}d up`;
  if (seconds >= 3600) return `${text.split('h')[0]}h up`;
  return `${Math.floor(seconds / 60)}m up`;
}

interface RowProps {
  name: 'cpu' | 'mem' | 'disk';
  percent: number | null;
  value: string;
}

function Row({ name, percent, value }: RowProps) {
  const clamped = percent === null ? 0 : Math.max(0, Math.min(100, percent));
  return (
    <div className={styles.row}>
      <span className={styles.name}>{name}</span>
      <span
        className={styles.track}
        data-bar={name}
        data-tone={percent === null ? undefined : tone(percent)}
        data-empty={percent === null ? 'true' : undefined}
      >
        <span className={styles.fill} style={{ width: `${clamped}%` }} />
      </span>
      <span className={styles.value}>{value}</span>
    </div>
  );
}

export function HostBlock() {
  const snapshot = useStore(useServer().host);
  if (!snapshot) {
    return (
      <div className={styles.block}>
        <span className={styles.placeholder}>…</span>
      </div>
    );
  }
  const { cpu, memory, disk, uptime } = snapshot;
  const cpuPercent = cpu?.percent ?? null;
  const memPercent = memory && memory.total > 0 ? (memory.used / memory.total) * 100 : null;
  const diskPercent = disk && disk.total > 0 ? ((disk.total - disk.free) / disk.total) * 100 : null;
  return (
    <div className={styles.block}>
      <div className={styles.head}>
        <Led tone="success" size={7} />
        <span className={styles.hostname}>{snapshot.hostname}</span>
        {uptime && <span className={styles.uptime}>{uptimeText(uptime.seconds)}</span>}
      </div>
      <Row name="cpu" percent={cpuPercent} value={cpuPercent === null ? '—' : `${cpuPercent.toFixed(1)}%`} />
      <Row name="mem" percent={memPercent} value={memory ? `${(memory.used / GIB).toFixed(1)}G` : '—'} />
      <Row name="disk" percent={diskPercent} value={diskPercent === null ? '—' : `${Math.round(diskPercent)}%`} />
    </div>
  );
}
