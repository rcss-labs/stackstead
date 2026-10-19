import styles from './Segmented.module.css';

export interface SegmentedProps {
  label: string;
  options: { value: string; label: string }[];
  value: string;
  onChange: (value: string) => void;
  size?: 'sm' | 'md';
}

export function Segmented({ label, options, value, onChange, size = 'md' }: SegmentedProps) {
  return (
    <div className={styles.group} role="group" aria-label={label} data-size={size}>
      {options.map((option) => (
        <button
          key={option.value}
          type="button"
          className={styles.option}
          aria-pressed={option.value === value}
          onClick={() => onChange(option.value)}
        >
          {option.label}
        </button>
      ))}
    </div>
  );
}
