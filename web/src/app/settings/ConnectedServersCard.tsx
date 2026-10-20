import { useEffect, useLayoutEffect, useRef, useState, type FormEvent } from 'react';
import { api, ApiError } from '$lib/api/client';
import type { ConnectionToken, PeerProbe, PeerTrust, PeerView, ServerSelf } from '$lib/api/types';
import { relativeTime } from '$lib/format';
import { peers, refreshPeers } from '$lib/servers/peers';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { CopyButton } from '../components/CopyButton';
import { Field } from '../components/Field';
import { Spinner } from '../components/Spinner';
import { useStore } from '../hooks/useStore';
import styles from './ConnectedServersCard.module.css';
import { Pill, SettingsCard } from './SettingsCard';

const PLAINTEXT_LABEL = 'I accept sending a root-equivalent credential unencrypted';
const NO_TRUST: PeerTrust = { caPem: '', pinSha256: '', plaintext: false };

const messageOf = (err: unknown) => (err instanceof ApiError ? err.message : 'Request failed.');
const fieldError = (err: unknown, name: string) =>
  err instanceof ApiError ? err.fields.find((f) => f.field === name)?.message : undefined;

function hostOf(url: string): string {
  try {
    return new URL(url).hostname;
  } catch {
    return url;
  }
}

/** Failures whose wording depends on which server was being added. */
function peerErrorCopy(err: unknown, name: string): string | undefined {
  if (!(err instanceof ApiError)) return undefined;
  if (err.code === 'peer_old') return `${name} runs an older Stackstead without connected servers`;
  if (err.code === 'peer_proxy') return `${name}'s proxy answered instead of Stackstead (it may require its own login, see docs)`;
  return undefined;
}

function ThisServer({ self, onSaved }: { self: ServerSelf; onSaved: (next: ServerSelf) => void }) {
  const [name, setName] = useState(self.name);
  const [advertised, setAdvertised] = useState(self.advertisedUrl);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>();

  async function save(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      onSaved(await api.updateServerSelf({ name, advertisedUrl: advertised }));
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  }

  const general = error && !fieldError(error, 'name') && !fieldError(error, 'advertisedUrl') ? messageOf(error) : undefined;
  return (
    <form className={styles.group} onSubmit={save}>
      <h3 className={styles.subtitle}>This server</h3>
      {general && (
        <Banner kind="danger" title="Could not save">
          {general}
        </Banner>
      )}
      <Field label="Server name" id="cs-self-name" error={fieldError(error, 'name')}>
        {({ readOnly: _, ...c }) => <input {...c} type="text" value={name} onChange={(e) => setName(e.target.value)} />}
      </Field>
      <Field label="Advertised address" id="cs-self-url" error={fieldError(error, 'advertisedUrl')} hint="How other servers reach this one.">
        {({ readOnly: _, ...c }) => (
          <div className={styles.cluster}>
            <input
              {...c}
              type="text"
              className={styles.tokenInput}
              value={advertised}
              placeholder={self.suggestedUrl}
              onChange={(e) => setAdvertised(e.target.value)}
            />
            <Button variant="secondary" disabled={!self.suggestedUrl} onClick={() => setAdvertised(self.suggestedUrl)}>
              Use suggested
            </Button>
          </div>
        )}
      </Field>
      <span className={styles.small}>Instance ID {self.instanceId}</span>
      <div>
        <Button variant="primary" type="submit" loading={busy}>
          Save
        </Button>
      </div>
    </form>
  );
}

function ConnectionTokenSection() {
  // The one-time secret lives only in this component's state: never a store, storage or log.
  const [issued, setIssued] = useState<ConnectionToken | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const input = useRef<HTMLInputElement>(null);

  // Set as a property so the token is never serialised into the markup.
  useLayoutEffect(() => {
    if (issued && input.current) input.current.value = issued.token;
  });

  async function create() {
    setBusy(true);
    setError(undefined);
    try {
      setIssued(await api.createConnectionToken());
    } catch (err) {
      setError(messageOf(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.group}>
      <h3 className={styles.subtitle}>Connect another server to this one</h3>
      {error && (
        <Banner kind="danger" title="Could not create a token">
          {error}
        </Banner>
      )}
      {issued ? (
        <>
          <Banner kind="warning" title="Copy this token now">
            It is shown only once and cannot be retrieved later.
          </Banner>
          <Field label="Connection token" id="cs-issued-token" mono hint={`Expires at ${new Date(issued.expiresAt).toLocaleString()}`}>
            {(control) => (
              <div className={styles.cluster}>
                <input {...control} ref={input} type="text" readOnly autoComplete="off" spellCheck={false} className={styles.tokenInput} />
                <CopyButton text={issued.token} />
              </div>
            )}
          </Field>
          <div>
            <Button variant="primary" onClick={() => setIssued(null)}>
              I have saved it
            </Button>
          </div>
        </>
      ) : (
        <div>
          <Button variant="secondary" loading={busy} onClick={create}>
            Create connection token
          </Button>
        </div>
      )}
    </div>
  );
}

function AddServer({ self }: { self: ServerSelf }) {
  const [url, setUrl] = useState('');
  const [token, setToken] = useState('');
  const [probe, setProbe] = useState<PeerProbe | null>(null);
  const [pinned, setPinned] = useState(false);
  const [caPem, setCaPem] = useState('');
  const [plaintext, setPlaintext] = useState(false);
  const [selfCaPem, setSelfCaPem] = useState('');
  const [busy, setBusy] = useState<'probe' | 'add' | null>(null);
  const [error, setError] = useState<unknown>();
  const host = hostOf(url);
  const advertisedUrl = self.advertisedUrl || self.suggestedUrl;

  function resetProbe() {
    setProbe(null);
    setPinned(false);
    setCaPem('');
    setPlaintext(false);
    setSelfCaPem('');
  }

  async function check() {
    setBusy('probe');
    setError(undefined);
    resetProbe();
    try {
      setProbe(await api.probePeer(url));
    } catch (err) {
      setError(err);
    } finally {
      setBusy(null);
    }
  }

  // Certificate trust is an https matter; an http address only needs the plaintext acceptance.
  const needsTrust = probe !== null && !probe.plaintextAllowed && !probe.systemTrusted;
  const trusted = !needsTrust || pinned || caPem.trim() !== '';
  const plaintextAccepted = !probe?.plaintextAllowed || plaintext;

  async function add(event: FormEvent) {
    event.preventDefault();
    if (!probe) return;
    const trust: PeerTrust = plaintext
      ? { ...NO_TRUST, plaintext: true }
      : { caPem: caPem.trim(), pinSha256: pinned ? probe.fingerprintSha256 : '', plaintext: false };
    setBusy('add');
    setError(undefined);
    try {
      // The remote must reach us over the same transport, so our side mirrors the plaintext choice.
      // With no CA given for this server, the backend pins the certificate it finds at its own address.
      await api.addPeer({ url, token, trust, selfTrust: { ...NO_TRUST, caPem: plaintext ? '' : selfCaPem.trim(), plaintext }, advertisedUrl });
      setUrl('');
      setToken('');
      resetProbe();
      void refreshPeers();
    } catch (err) {
      setError(err);
    } finally {
      setBusy(null);
    }
  }

  const special = peerErrorCopy(error, host);
  // Fields whose input is on screen show their own error; anything else goes in the banner.
  const shown = ['url', 'token', ...(needsTrust ? ['trust.pinSha256'] : []), ...(probe?.plaintextAllowed ? ['trust.plaintext'] : []), ...(probe ? ['advertisedUrl'] : [])];
  const leftover = error instanceof ApiError ? error.fields.filter((f) => !shown.includes(f.field)) : [];
  const general =
    error && !special
      ? error instanceof ApiError && error.fields.length > 0
        ? leftover.map((f) => f.message).join(' ') || undefined
        : messageOf(error)
      : undefined;
  const advertisedError = fieldError(error, 'advertisedUrl');

  return (
    <form className={styles.group} onSubmit={add}>
      <h3 className={styles.subtitle}>Add a server</h3>
      {(special || general) && (
        <Banner kind="danger" title="Could not add the server">
          {special ?? general}
        </Banner>
      )}
      <Field label="Server address" id="cs-add-url" error={fieldError(error, 'url')}>
        {({ readOnly: _, ...c }) => (
          <input
            {...c}
            type="text"
            value={url}
            placeholder="https://other-server:8443"
            onChange={(e) => {
              setUrl(e.target.value);
              resetProbe();
            }}
          />
        )}
      </Field>
      <Field label="Connection token" id="cs-add-token" error={fieldError(error, 'token')} hint="Created on the other server.">
        {({ readOnly: _, ...c }) => (
          <input {...c} type="password" autoComplete="off" value={token} onChange={(e) => setToken(e.target.value)} />
        )}
      </Field>
      <div>
        <Button variant="secondary" loading={busy === 'probe'} disabledReason={url.trim() ? undefined : 'Enter the server address first.'} onClick={check}>
          Check address
        </Button>
      </div>
      {probe && (
        <>
          {needsTrust && (
            <>
              <p>
                This server&apos;s certificate is not signed by a trusted authority. Fingerprint (SHA-256):
              </p>
              <code className={styles.fingerprint}>{probe.fingerprintSha256}</code>
              <Field label="Trust this certificate" id="cs-add-pin" error={fieldError(error, 'trust.pinSha256')}>
                {({ readOnly: _, ...c }) => (
                  <input {...c} type="checkbox" checked={pinned} disabled={plaintext} onChange={(e) => setPinned(e.target.checked)} />
                )}
              </Field>
              <Field label="Or paste its CA certificate (PEM)" id="cs-add-ca">
                {({ readOnly: _, ...c }) => (
                  <textarea {...c} className={styles.pem} value={caPem} disabled={plaintext} onChange={(e) => setCaPem(e.target.value)} />
                )}
              </Field>
            </>
          )}
          {probe.plaintextAllowed && (
            <Field label={PLAINTEXT_LABEL} id="cs-add-plaintext" error={fieldError(error, 'trust.plaintext')}>
              {({ readOnly: _, ...c }) => (
                <input {...c} type="checkbox" checked={plaintext} onChange={(e) => setPlaintext(e.target.checked)} />
              )}
            </Field>
          )}
          <p>{`${host} will reach this server at ${advertisedUrl}`}</p>
          {!plaintext && (
            <Field label="This server's CA certificate (PEM)" id="cs-add-self-ca" hint="Only if this server's certificate is signed by a private CA; otherwise its certificate is pinned automatically.">
              {({ readOnly: _, ...c }) => (
                <textarea {...c} className={styles.pem} value={selfCaPem} onChange={(e) => setSelfCaPem(e.target.value)} />
              )}
            </Field>
          )}
          {advertisedError && <Banner kind="danger" title="Advertised address rejected">{advertisedError}</Banner>}
          <div>
            <Button
              variant="primary"
              type="submit"
              loading={busy === 'add'}
              disabledReason={!advertisedUrl ? 'Set this server\'s advertised address under "This server" first.' : !plaintextAccepted ? 'Accept sending the credential unencrypted first.' : !trusted ? 'Trust the certificate or paste its CA first.' : !token ? 'Enter the connection token first.' : undefined}
            >
              Add server
            </Button>
          </div>
        </>
      )}
    </form>
  );
}

function PeerRow({ peer, onRemove }: { peer: PeerView; onRemove: () => void }) {
  const removed = peer.status === 'removed';
  return (
    <li className={styles.peer}>
      <div className={styles.peerMain}>
        <span className={styles.peerName}>{peer.name}</span>
        <span className={styles.meta}>
          {peer.url}
          {peer.version && ` · ${/^v?\d+\.\d+/.test(peer.version) ? `v${peer.version.replace(/^v/, '')}` : peer.version}`}
          {peer.lastSeenAt && ` · seen ${relativeTime(peer.lastSeenAt, new Date())}`}
        </span>
        {removed && <span>{`${peer.name} removed this connection`}</span>}
        {peer.lastError && !removed && <span className={styles.meta}>{peer.lastError}</span>}
      </div>
      <Pill tone={peer.status === 'reachable' ? 'success' : removed || peer.status === 'pending' ? 'muted' : 'danger'}>{peer.status}</Pill>
      {removed ? (
        <Button variant="secondary" size="sm" onClick={onRemove}>
          Dismiss
        </Button>
      ) : (
        <Button variant="danger" size="sm" aria-label={`Remove ${peer.name}`} onClick={onRemove}>
          Remove
        </Button>
      )}
    </li>
  );
}

export function ConnectedServersCard() {
  const [self, setSelf] = useState<ServerSelf | null>(null);
  const [loadError, setLoadError] = useState<string>();
  const [removing, setRemoving] = useState<PeerView | null>(null);
  const [removeBusy, setRemoveBusy] = useState(false);
  const [removeError, setRemoveError] = useState<string>();
  const { items } = useStore(peers);
  const started = useRef(false);

  async function load() {
    setLoadError(undefined);
    try {
      setSelf(await api.serverSelf());
    } catch (err) {
      setLoadError(messageOf(err));
    }
  }
  useEffect(() => {
    if (!started.current) {
      started.current = true; // StrictMode runs effects twice
      void load();
    }
  }, []);

  async function remove(peer: PeerView) {
    setRemoveBusy(true);
    setRemoveError(undefined);
    try {
      await api.removePeer(peer.id);
      setRemoving(null);
      await refreshPeers();
    } catch (err) {
      setRemoveError(messageOf(err));
      setRemoving(null);
    } finally {
      setRemoveBusy(false);
    }
  }

  return (
    <>
      <SettingsCard id="servers-h" title="Connected servers" footer="Connected servers share a root-equivalent credential; only connect servers you control.">
        {loadError ? (
          <>
            <Banner kind="danger" title="Could not load this server">
              {loadError}
            </Banner>
            <div>
              <Button variant="secondary" onClick={() => void load()}>
                Retry
              </Button>
            </div>
          </>
        ) : !self ? (
          <Spinner />
        ) : (
          <>
            <ThisServer key={self.instanceId} self={self} onSaved={setSelf} />
            <ConnectionTokenSection />
            <AddServer self={self} />
            <div className={styles.group}>
              <h3 className={styles.subtitle}>Servers</h3>
              {removeError && (
                <Banner kind="danger" title="Could not remove the server">
                  {removeError}
                </Banner>
              )}
              {items.length === 0 ? (
                <p className={styles.meta}>No connected servers.</p>
              ) : (
                <ul className={styles.peers}>
                  {items.map((peer) => (
                    <PeerRow
                      key={peer.id}
                      peer={peer}
                      onRemove={() => (peer.status === 'removed' ? void remove(peer) : setRemoving(peer))}
                    />
                  ))}
                </ul>
              )}
            </div>
          </>
        )}
      </SettingsCard>
      <ConfirmDialog
        open={removing !== null}
        title="Remove server"
        message={`Remove ${removing?.name ?? ''}? Its stacks disappear from this dashboard and the other server is told the connection is gone.`}
        confirmLabel="Remove"
        danger
        pending={removeBusy}
        onConfirm={() => removing && void remove(removing)}
        onCancel={() => setRemoving(null)}
      />
    </>
  );
}
