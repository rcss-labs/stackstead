const BYTE_UNITS = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'];

/** 1536 -> "1.5 KiB". Negative and non-finite input is shown as "0 B". */
export function formatBytes(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return '0 B';
  let value = n;
  let unit = 0;
  while (value >= 1024 && unit < BYTE_UNITS.length - 1) {
    value /= 1024;
    unit++;
  }
  return unit === 0 ? `${Math.floor(value)} B` : `${value.toFixed(1)} ${BYTE_UNITS[unit]}`;
}

export function formatRate(bytesPerSec: number): string {
  return `${formatBytes(bytesPerSec)}/s`;
}

/** Two most significant units: "2m 5s", "1h 2m", "1d 1h". */
export function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return '0s';
  const total = Math.floor(ms / 1000);
  const d = Math.floor(total / 86400);
  const h = Math.floor((total % 86400) / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m ${s}s`;
  return `${s}s`;
}

/** "5m ago"; future timestamps (clock skew) and sub-5s ages are "just now"; unparseable is "—". */
export function relativeTime(iso: string, now: Date): string {
  const then = Date.parse(iso);
  if (Number.isNaN(then)) return '—';
  const sec = Math.floor((now.getTime() - then) / 1000);
  if (sec < 5) return 'just now';
  if (sec < 60) return `${sec}s ago`;
  if (sec < 3600) return `${Math.floor(sec / 60)}m ago`;
  if (sec < 86400) return `${Math.floor(sec / 3600)}h ago`;
  return `${Math.floor(sec / 86400)}d ago`;
}

/** Input is already a percentage (12.3 -> "12.3%"). */
export function formatPercent(x: number): string {
  if (!Number.isFinite(x) || x < 0) return '0.0%';
  return `${x.toFixed(1)}%`;
}

const trim = (n: number) => String(Number(n.toFixed(1)));

const PAD = 1;

/**
 * SVG path for a sparkline in a w x h box with 1 unit of padding top and bottom, scaled so
 * the largest value touches the top padding line and zero sits on the bottom one.
 * Null samples break the line ("M…L…M…"); a lone sample between nulls is a short horizontal
 * segment; a single sample in the whole series is a flat line across the width (on the
 * baseline when it is 0, like any all-zero series); "" when there is nothing to draw.
 */
export function sparklinePath(values: (number | null)[], w = 60, h = 18): string {
  const present = values.filter((v): v is number => v !== null);
  if (present.length === 0) return '';
  const max = Math.max(...present);
  const y = (v: number) => (max > 0 ? PAD + (1 - v / max) * (h - 2 * PAD) : h - PAD);
  if (present.length === 1) {
    const flat = y(present[0]);
    return `M0,${trim(flat)}L${trim(w)},${trim(flat)}`;
  }
  const stepX = values.length > 1 ? w / (values.length - 1) : 0;
  let path = '';
  let open = false;
  values.forEach((v, i) => {
    if (v === null) {
      open = false;
      return;
    }
    const x = i * stepX;
    const lone = values[i - 1] == null && values[i + 1] == null;
    if (lone) {
      path += `M${trim(Math.max(0, x - 1))},${trim(y(v))}L${trim(Math.min(w, x + 1))},${trim(y(v))}`;
      return;
    }
    path += `${open ? 'L' : 'M'}${trim(x)},${trim(y(v))}`;
    open = true;
  });
  return path;
}
