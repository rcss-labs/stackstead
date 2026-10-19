import { sparklinePath } from '$lib/format';

export interface SparklineProps {
  values: (number | null)[];
  tone: 'accent' | 'info' | 'success' | 'warning' | 'danger';
  width?: number;
  height?: number;
}

const VIEW_W = 60;
const VIEW_H = 18;

export function Sparkline({ values, tone, width = 56, height = 18 }: SparklineProps) {
  // No samples at all: a flat line, so the cell still reads as a sparkline.
  const d = sparklinePath(values, VIEW_W, VIEW_H) || 'M0,17L60,17';
  return (
    <svg
      width={width}
      height={height}
      viewBox={`0 0 ${VIEW_W} ${VIEW_H}`}
      preserveAspectRatio="none"
      aria-hidden="true"
      style={{ flex: 'none' }}
    >
      <path
        d={d}
        fill="none"
        stroke={`var(--${tone})`}
        strokeWidth={1.2}
        strokeLinejoin="round"
        strokeLinecap="round"
        vectorEffect="non-scaling-stroke"
      />
    </svg>
  );
}
