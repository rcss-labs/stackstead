import { Chip } from '../components/Chip';
import styles from './StorageTable.module.css';

export interface KindToolbarProps {
  chips: { id: string; label: string }[];
  counts: Record<string, number>;
  pressed: string;
  onChip: (id: string) => void;
  /** Also the chip group name and search placeholder, e.g. `Filter images`. */
  searchLabel: string;
  search: string;
  onSearch: (value: string) => void;
}

export function KindToolbar({ chips, counts, pressed, onChip, searchLabel, search, onSearch }: KindToolbarProps) {
  return (
    <div className={styles.toolbar}>
      <div role="group" aria-label={searchLabel} className={styles.chips}>
        {chips.map((chip) => (
          <Chip key={chip.id} pressed={chip.id === pressed} count={counts[chip.id]} onClick={() => onChip(chip.id)}>
            {chip.label}
          </Chip>
        ))}
      </div>
      <label className={styles.search}>
        <input type="search" aria-label={searchLabel} placeholder={searchLabel} value={search} onChange={(event) => onSearch(event.target.value)} />
      </label>
    </div>
  );
}
