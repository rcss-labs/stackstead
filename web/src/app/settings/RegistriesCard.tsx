import { useCallback, useEffect, useId, useState, type FormEvent } from 'react';
import { createLocalImagesApi } from '$lib/api/images';
import type { RegistryECR, RegistryRequest, RegistryTest, RegistryView } from '$lib/api/imageTypes';
import { relativeTime } from '$lib/format';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { Dialog } from '../components/Dialog';
import { Field } from '../components/Field';
import { Segmented } from '../components/Segmented';
import styles from './RegistriesCard.module.css';
import { Pill, SettingsCard } from './SettingsCard';

const api = createLocalImagesApi();
const messageOf = (e: unknown) => (e instanceof Error ? e.message : String(e));

const KINDS = [
  { value: 'dockerhub', label: 'Docker Hub' },
  { value: 'ecr', label: 'Amazon ECR' },
];
const MODES = [
  { value: 'static', label: 'Access key' },
  { value: 'role', label: 'IAM role' },
  { value: 'profile', label: 'AWS profile' },
];
const kindLabel = (kind: string) => KINDS.find((k) => k.value === kind)?.label ?? kind;

function RegistryDialog({ registry, onSaved, onClose }: { registry?: RegistryView; onSaved(id: string): void; onClose(): void }) {
  const uid = useId();
  const editing = registry !== undefined;
  const [name, setName] = useState(registry?.name ?? '');
  const [kind, setKind] = useState(registry?.kind ?? 'dockerhub');
  const [username, setUsername] = useState(registry?.username ?? '');
  const [token, setToken] = useState('');
  const [region, setRegion] = useState(registry?.region ?? '');
  const [mode, setMode] = useState(registry?.mode || 'static');
  const [accessKeyId, setAccessKeyId] = useState(registry?.accessKeyId ?? '');
  const [secretAccessKey, setSecretAccessKey] = useState('');
  const [sessionToken, setSessionToken] = useState('');
  const [profile, setProfile] = useState(registry?.profile ?? '');
  const [error, setError] = useState<string>();
  const [pending, setPending] = useState(false);
  const keepHint = editing ? 'Leave blank to keep the stored value.' : undefined;

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (pending) return;
    const request: RegistryRequest = { name: name.trim(), kind };
    if (kind === 'dockerhub') request.hub = { username: username.trim(), token };
    else {
      // Only the chosen mode's fields are sent; blank secrets mean "keep" on edit.
      const ecr: RegistryECR = { region: region.trim(), mode, accessKeyId: '', secretAccessKey: '', sessionToken: '', profile: '' };
      if (mode === 'static') Object.assign(ecr, { accessKeyId: accessKeyId.trim(), secretAccessKey, sessionToken });
      if (mode === 'profile') ecr.profile = profile.trim();
      request.ecr = ecr;
    }
    setPending(true);
    setError(undefined);
    try {
      if (registry) await api.updateRegistry(registry.id, request);
      else await api.createRegistry(request);
      onSaved(registry?.id ?? '');
    } catch (e) {
      setError(messageOf(e));
      setPending(false);
    }
  }

  return (
    <Dialog open title={editing ? 'Edit registry' : 'Add registry'} onClose={onClose}>
      <form className={styles.form} onSubmit={(e) => void submit(e)}>
        <Field label="Name" id={`${uid}-name`}>
          {(c) => <input {...c} value={name} maxLength={64} onChange={(e) => setName(e.target.value)} />}
        </Field>
        {editing ? (
          <p className={styles.meta}>{kindLabel(kind)}</p>
        ) : (
          <Segmented label="Registry kind" options={KINDS} value={kind} onChange={setKind} />
        )}
        {kind === 'dockerhub' ? (
          <>
            <Field label="Username" id={`${uid}-user`} mono>
              {(c) => <input {...c} value={username} autoComplete="off" onChange={(e) => setUsername(e.target.value)} />}
            </Field>
            <Field label="Token" id={`${uid}-token`} hint={keepHint}>
              {({ readOnly: _, ...c }) => <input {...c} type="password" value={token} autoComplete="new-password" onChange={(e) => setToken(e.target.value)} />}
            </Field>
          </>
        ) : (
          <>
            <Field label="Region" id={`${uid}-region`} mono>
              {(c) => <input {...c} value={region} placeholder="eu-west-1" onChange={(e) => setRegion(e.target.value)} />}
            </Field>
            <Segmented label="Credentials" options={MODES} value={mode} onChange={setMode} />
            {mode === 'static' && (
              <>
                <Field label="Access key ID" id={`${uid}-akid`} mono>
                  {(c) => <input {...c} value={accessKeyId} autoComplete="off" onChange={(e) => setAccessKeyId(e.target.value)} />}
                </Field>
                <Field label="Secret access key" id={`${uid}-secret`} hint={keepHint}>
                  {({ readOnly: _, ...c }) => <input {...c} type="password" value={secretAccessKey} autoComplete="new-password" onChange={(e) => setSecretAccessKey(e.target.value)} />}
                </Field>
                <Field label="Session token" id={`${uid}-session`} hint={editing ? 'Optional. Leave blank to keep the stored value.' : 'Optional.'}>
                  {({ readOnly: _, ...c }) => <input {...c} type="password" value={sessionToken} autoComplete="new-password" onChange={(e) => setSessionToken(e.target.value)} />}
                </Field>
              </>
            )}
            {mode === 'role' && <p className={styles.meta}>Uses the IAM role of the host this server runs on.</p>}
            {mode === 'profile' && (
              <Field label="Profile" id={`${uid}-profile`} mono hint="A profile with static keys in the host's AWS credentials file.">
                {(c) => <input {...c} value={profile} onChange={(e) => setProfile(e.target.value)} />}
              </Field>
            )}
          </>
        )}
        {error && (
          <Banner kind="danger" title="Could not save">
            {error}
          </Banner>
        )}
        <div className={styles.actions}>
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" type="submit" loading={pending} disabledReason={name.trim() === '' ? 'Enter a name' : undefined}>
            Save
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

export function RegistriesCard() {
  const [registries, setRegistries] = useState<RegistryView[]>();
  const [loadError, setLoadError] = useState<string>();
  const [dialog, setDialog] = useState<{ registry?: RegistryView }>();
  const [deleting, setDeleting] = useState<RegistryView>();
  const [pending, setPending] = useState(false);
  const [testing, setTesting] = useState<string>();
  const [results, setResults] = useState<Record<string, RegistryTest | string>>({});

  const load = useCallback(() => {
    api
      .registries()
      .then((list) => {
        setRegistries(list);
        setLoadError(undefined);
      })
      .catch((e: unknown) => setLoadError(messageOf(e)));
  }, []);
  useEffect(load, [load]);

  async function test(registry: RegistryView) {
    setTesting(registry.id);
    try {
      const result = await api.testRegistry(registry.id);
      setResults((prev) => ({ ...prev, [registry.id]: result }));
    } catch (e) {
      setResults((prev) => ({ ...prev, [registry.id]: messageOf(e) }));
    } finally {
      setTesting(undefined);
    }
  }

  async function remove(registry: RegistryView) {
    setPending(true);
    try {
      await api.deleteRegistry(registry.id);
      setDeleting(undefined);
      load();
    } catch (e) {
      setLoadError(messageOf(e));
      setDeleting(undefined);
    } finally {
      setPending(false);
    }
  }

  return (
    <SettingsCard
      id="registries-h"
      title="Registries"
      headerEnd={
        <Button variant="secondary" size="xs" onClick={() => setDialog({})}>
          Add registry
        </Button>
      }
      footer="Images are pushed to a registry from an image's push targets. Tokens and keys are stored on this server and never shown again."
    >
      {loadError && (
        <Banner kind="danger" title="Registries">
          {loadError}
        </Banner>
      )}
      {registries?.length === 0 && <p>No registries yet.</p>}
      <ul className={styles.list}>
        {registries?.map((r) => {
          const result = results[r.id];
          return (
            <li key={r.id} className={styles.item}>
              <div className={styles.main}>
                <span className={styles.name}>{r.name}</span>
                <span className={styles.meta}>{kindLabel(r.kind)}</span>
                <span className={styles.meta}>{r.host}</span>
                {r.lastTestAt && (
                  <span className={styles.meta}>
                    Last test {relativeTime(r.lastTestAt, new Date())}: {r.lastTestOk ? 'ok' : r.lastTestMessage || 'failed'}
                  </span>
                )}
                {result !== undefined && (
                  <span className={styles.result} role="status">
                    {typeof result === 'string' ? result : `${result.ok ? 'Connected' : 'Failed'}: ${result.message}`}
                  </span>
                )}
              </div>
              <div className={styles.cluster}>
                {!r.configured && <Pill tone="danger">Not configured</Pill>}
                <Button variant="secondary" size="xs" aria-label={`Test ${r.name}`} loading={testing === r.id} onClick={() => void test(r)}>
                  Test connection
                </Button>
                <Button variant="secondary" size="xs" aria-label={`Edit ${r.name}`} onClick={() => setDialog({ registry: r })}>
                  Edit
                </Button>
                <Button variant="danger" size="xs" aria-label={`Delete ${r.name}`} onClick={() => setDeleting(r)}>
                  Delete
                </Button>
              </div>
            </li>
          );
        })}
      </ul>
      {dialog && (
        <RegistryDialog
          registry={dialog.registry}
          onClose={() => setDialog(undefined)}
          onSaved={(id) => {
            setResults(({ [id]: _stale, ...rest }) => rest);
            setDialog(undefined);
            load();
          }}
        />
      )}
      <ConfirmDialog
        open={deleting !== undefined}
        title="Delete registry"
        message={`Delete ${deleting?.name ?? ''}? Push targets that use it stop working.`}
        confirmLabel="Delete registry"
        danger
        pending={pending}
        onConfirm={() => deleting && void remove(deleting)}
        onCancel={() => setDeleting(undefined)}
      />
    </SettingsCard>
  );
}
