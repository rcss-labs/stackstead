import { Button } from './Button';
import styles from './Pager.module.css';

export interface PagerProps {
  page: number;
  pages: number;
  from: number;
  to: number;
  total: number;
  label: string;
  onPage: (page: number) => void;
}

export function Pager({ page, pages, from, to, total, label, onPage }: PagerProps) {
  if (pages <= 1) return null;
  return (
    <nav className={styles.pager} aria-label={`${label} pages`}>
      <Button variant="secondary" size="sm" disabled={page <= 1} onClick={() => onPage(page - 1)}>
        Previous
      </Button>
      <span className={styles.status} aria-live="polite">
        {from}–{to} of {total}
      </span>
      <Button variant="secondary" size="sm" disabled={page >= pages} onClick={() => onPage(page + 1)}>
        Next
      </Button>
    </nav>
  );
}
