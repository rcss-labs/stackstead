import styles from './EditSteps.module.css';

const STEPS = ['Edit', 'Review changes', 'Apply'];

/** Where the user is in edit, review, apply. Text only; the phase logic stays with each editor. */
export function EditSteps({ current }: { current: 1 | 2 | 3 }) {
  return (
    <ol className={styles.steps} aria-label="Edit progress">
      {STEPS.map((label, i) => (
        <li key={label} aria-current={i + 1 === current ? 'step' : undefined} className={styles.step}>
          {`${i + 1} ${label}`}
        </li>
      ))}
    </ol>
  );
}
