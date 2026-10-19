import styles from './Segmented.module.css';

export interface SegmentedProps {
  label: string;
  options: { value: string; label: string; ariaLabel?: string }[];
  value: string;
  onChange: (value: string) => void;
  size?: 'sm' | 'md';
  /** Extra class on the group, for a local restyle. */
  className?: string;
}

export function Segmented({ label, options, value, onChange, size = 'md', className }: SegmentedProps) {
  return (
    <div className={className ? `${styles.group} ${className}` : styles.group} role="group" aria-label={label} data-size={size}>
      {options.map((option) => (
        <button
          key={option.value}
          type="button"
          className={styles.option}
          aria-label={option.ariaLabel}
          aria-pressed={option.value === value}
          onClick={() => onChange(option.value)}
        >
          {option.label}
        </button>
      ))}
    </div>
  );
}
