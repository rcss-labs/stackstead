import { useCallback, useEffect, useId, useState, type FormEvent } from 'react';
import { createLocalImagesApi } from '$lib/api/images';
import type { ImageDefaults } from '$lib/api/imageTypes';
import { retentionText } from '$lib/images/retention';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { Field } from '../components/Field';
import styles from './RegistriesCard.module.css';
import { SettingsCard } from './SettingsCard';

const api = createLocalImagesApi();
const messageOf = (e: unknown) => (e instanceof Error ? e.message : String(e));
const MAX_KEEP_LAST = 1000;
const MAX_KEEP_DAYS = 3650;

function parseCount(text: string, max: number): number | undefined {
  const t = text.trim();
  if (!/^\d+$/.test(t)) return undefined;
  const n = Number(t);
  return n <= max ? n : undefined;
}

/** The retention given to new images; existing images keep their own policy. */
export function ImageDefaultsCard() {
  const uid = useId();
  const [defaults, setDefaults] = useState<ImageDefaults>();
  const [keepLast, setKeepLast] = useState('0');
  const [keepDays, setKeepDays] = useState('0');
  const [error, setError] = useState<string>();
  const [saving, setSaving] = useState(false);

  const apply = useCallback((d: ImageDefaults) => {
    setDefaults(d);
    setKeepLast(String(d.retention.keepLast));
    setKeepDays(String(d.retention.keepDays));
  }, []);

  useEffect(() => {
    api.imageDefaults().then(apply, (e: unknown) => setError(messageOf(e)));
  }, [apply]);

  const last = parseCount(keepLast, MAX_KEEP_LAST);
  const days = parseCount(keepDays, MAX_KEEP_DAYS);

  async function save(event: FormEvent) {
    event.preventDefault();
    if (saving || last === undefined || days === undefined) return;
    setSaving(true);
    setError(undefined);
    try {
      apply(await api.setImageDefaults({ retention: { keepLast: last, keepDays: days } }));
    } catch (e) {
      setError(messageOf(e));
    } finally {
      setSaving(false);
    }
  }

  return (
    <SettingsCard
      id="images-h"
      title="Images"
      footer="Applies to images created from now on; existing images keep their own policy. Retention never removes the newest successful version or a version in use, and only cleans up on this server."
    >
      {error && (
        <Banner kind="danger" title="Images">
          {error}
        </Banner>
      )}
      {defaults && <p role="status">{retentionText(defaults.retention)}</p>}
      <form className={styles.form} onSubmit={(e) => void save(e)}>
        <Field
          label="Keep last"
          id={`${uid}-last`}
          hint="Versions to keep per image; 0 turns this rule off."
          error={last === undefined ? `Enter a whole number from 0 to ${MAX_KEEP_LAST}` : undefined}
        >
          {({ readOnly: _, ...c }) => <input {...c} inputMode="numeric" value={keepLast} onChange={(e) => setKeepLast(e.target.value)} />}
        </Field>
        <Field
          label="Keep days"
          id={`${uid}-days`}
          hint="Keep versions built within this many days; 0 turns this rule off."
          error={days === undefined ? `Enter a whole number from 0 to ${MAX_KEEP_DAYS}` : undefined}
        >
          {({ readOnly: _, ...c }) => <input {...c} inputMode="numeric" value={keepDays} onChange={(e) => setKeepDays(e.target.value)} />}
        </Field>
        <div className={styles.actions}>
          <Button
            variant="primary"
            type="submit"
            loading={saving}
            disabledReason={last === undefined || days === undefined ? 'Fix the highlighted values' : undefined}
          >
            Save default
          </Button>
        </div>
      </form>
    </SettingsCard>
  );
}
