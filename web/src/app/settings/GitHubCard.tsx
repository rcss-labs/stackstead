import { useCallback, useEffect, useId, useState, type FormEvent } from 'react';
import { createLocalImagesApi } from '$lib/api/images';
import type { GitHubSettings, GitHubTest } from '$lib/api/imageTypes';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { Field } from '../components/Field';
import styles from './RegistriesCard.module.css';
import { Pill, SettingsCard } from './SettingsCard';

const api = createLocalImagesApi();
const messageOf = (e: unknown) => (e instanceof Error ? e.message : String(e));

/** The GitHub token is write-only: the field is never prefilled and only `{token}` is sent. */
export function GitHubCard() {
  const uid = useId();
  const [settings, setSettings] = useState<GitHubSettings>();
  const [token, setToken] = useState('');
  const [error, setError] = useState<string>();
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);
  const [result, setResult] = useState<GitHubTest | string>();

  const load = useCallback(() => {
    api.githubSettings().then(setSettings, (e: unknown) => setError(messageOf(e)));
  }, []);
  useEffect(load, [load]);

  async function save(event: FormEvent) {
    event.preventDefault();
    if (saving) return;
    setSaving(true);
    setError(undefined);
    try {
      setSettings(await api.setGitHubToken(token.trim()));
      setToken('');
      setResult(undefined);
    } catch (e) {
      setError(messageOf(e));
    } finally {
      setSaving(false);
    }
  }

  async function test() {
    setTesting(true);
    try {
      setResult(await api.testGitHub());
    } catch (e) {
      setResult(messageOf(e));
    } finally {
      setTesting(false);
    }
  }

  return (
    <SettingsCard
      id="github-h"
      title="GitHub"
      footer="A personal access token that can read the repositories your images build from. It is stored on this server and never shown again."
    >
      {error && (
        <Banner kind="danger" title="GitHub">
          {error}
        </Banner>
      )}
      {settings &&
        (settings.configured ? <Pill tone="success">{`Signed in as ${settings.login}`}</Pill> : <Pill tone="muted">Not configured</Pill>)}
      <form className={styles.form} onSubmit={(e) => void save(e)}>
        <Field label="Token" id={`${uid}-token`} hint={settings?.configured ? 'Saving replaces the stored token.' : undefined}>
          {({ readOnly: _, ...c }) => <input {...c} type="password" value={token} autoComplete="new-password" onChange={(e) => setToken(e.target.value)} />}
        </Field>
        <div className={styles.actions}>
          <Button variant="secondary" loading={testing} disabledReason={settings?.configured ? undefined : 'Save a token first'} onClick={() => void test()}>
            Test connection
          </Button>
          <Button variant="primary" type="submit" loading={saving} disabledReason={token.trim() === '' ? 'Enter a token' : undefined}>
            Save token
          </Button>
        </div>
      </form>
      {result !== undefined && (
        <span className={styles.result} role="status">
          {typeof result === 'string' ? result : `${result.ok ? 'Connected' : 'Failed'}: ${result.message}`}
        </span>
      )}
    </SettingsCard>
  );
}
