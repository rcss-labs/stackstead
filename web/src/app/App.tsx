import { Suspense, useEffect, useRef, useState } from 'react';
import type { ReactNode, RefObject } from 'react';
import { BrowserRouter, Route, Routes, useLocation, useNavigate } from 'react-router';
import { api, onUnauthorized } from '$lib/api/client';
import type { Operation } from '$lib/api/types';
import { bootstrapSession, session } from '$lib/api/session';
import { needsAttention } from '$lib/stacks/summary';
import { startHost } from '$lib/stores/host';
import { changes, live, startLive, stopLive } from '$lib/stores/live';
import { startStacks, stacks } from '$lib/stores/stacks';
import { toasts } from '$lib/stores/toasts';
import { Banner } from './components/Banner';
import { ConfirmDialog } from './components/ConfirmDialog';
import { Spinner } from './components/Spinner';
import { useDocumentTitle } from './hooks/useDocumentTitle';
import { useFocusOnNavigate } from './hooks/useFocusOnNavigate';
import { useRunningOps } from './hooks/useRunningOps';
import { useSessionGuard, useSessionLost } from './hooks/useSessionGuard';
import { useStore } from './hooks/useStore';
import { useGlobalShortcut } from './hooks/useGlobalShortcut';
import { useUnsavedGuard } from './hooks/useUnsavedGuard';
import { ROUTES, findRoute } from './routes';
import { PaletteHost } from './shell/PaletteHost';
import { Shell } from './shell/Shell';
import { Toasts } from './shell/Toasts';
import styles from './App.module.css';

const LoginScreen = ROUTES.find((r) => r.path === '/login')!.screen;
const SetupScreen = ROUTES.find((r) => r.path === '/setup')!.screen;

export function App() {
  return (
    <BrowserRouter>
      <AppFrame />
    </BrowserRouter>
  );
}

function AppFrame() {
  const { state } = useStore(session);
  const { pathname } = useLocation();
  // The session store may hold a previous state; nothing is decided until this page load has asked the server.
  const [booted, setBooted] = useState(false);
  const bootStarted = useRef(false);
  const loggingOut = useRef(false);
  const sessionLost = useSessionLost();
  const authenticated = state === 'authenticated';

  useEffect(() => {
    onUnauthorized(sessionLost);
    // StrictMode replays mount effects; the server is asked once per page load.
    if (bootStarted.current) return;
    bootStarted.current = true;
    bootstrapSession()
      .catch(() => {
        toasts.push({ kind: 'danger', message: 'Could not reach the Stackstead server' });
      })
      .finally(() => setBooted(true));
  }, [sessionLost]);

  useSessionGuard({ booted, loggingOut });
  const runningOps = useRunningOps(authenticated);
  useDocumentTitle(findRoute(pathname), authenticated);

  // Live updates, the stack list and the host poller live exactly as long as the session.
  useEffect(() => {
    if (!authenticated) return;
    startLive({ fetchStatus: api.status });
    const stopStacks = startStacks({ list: api.stacks, changes });
    const releaseHost = startHost({ host: api.host });
    return () => {
      releaseHost();
      stopStacks();
      stopLive();
    };
  }, [authenticated]);

  let page: ReactNode;
  if (!booted || state === 'unknown') {
    page = (
      <main className={styles.loading}>
        <Spinner label="Loading Stackstead" />
      </main>
    );
  } else if (state === 'needs-setup' || state === 'anonymous') {
    const Screen = state === 'needs-setup' ? SetupScreen : LoginScreen;
    page = (
      <main className={styles.bare}>
        <Suspense fallback={<Spinner label="Loading page" />}>
          <Screen />
        </Suspense>
      </main>
    );
  } else {
    page = <SignedInFrame runningOps={runningOps} loggingOut={loggingOut} />;
  }

  return (
    <>
      {page}
      <Toasts />
    </>
  );
}

interface SignedInFrameProps {
  runningOps: Operation[];
  loggingOut: RefObject<boolean>;
}

/** The shell, banners and the single main landmark; mounted only while signed in. */
function SignedInFrame({ runningOps, loggingOut }: SignedInFrameProps) {
  const navigate = useNavigate();
  const { username } = useStore(session);
  const liveState = useStore(live);
  const { items } = useStore(stacks);
  const mainRef = useRef<HTMLElement>(null);
  const [paletteOpen, setPaletteOpen] = useState(false);
  useGlobalShortcut(() => setPaletteOpen(true));
  const guard = useUnsavedGuard({ navigate, loggingOut });
  useFocusOnNavigate(mainRef);

  const banners: ReactNode[] = [];
  if (liveState.events === 'disconnected' || liveState.docker === 'unavailable') {
    banners.push(
      <Banner key="docker" kind="danger" title="Docker is unreachable">
        Status may be stale; lifecycle actions are disabled.
      </Banner>,
    );
  }
  if (liveState.compose === 'unavailable') {
    banners.push(
      <Banner key="compose" kind="warning" title="docker compose plugin not found">
        Read-only mode: stacks can be viewed but not started or stopped.
      </Banner>,
    );
  }
  if (liveState.metricsDegraded) {
    banners.push(
      <Banner key="metrics" kind="warning" title="Metrics are degraded">
        Charts may have gaps until collection recovers.
      </Banner>,
    );
  }
  if (liveState.connection === 'reconnecting') {
    banners.push(
      <Banner key="reconnecting" kind="info" title="Live updates reconnecting">
        Showing the last known state.
      </Banner>,
    );
  }

  return (
    <>
      <div className={styles.frame} onClickCapture={guard.onClickCapture}>
        <Shell
          username={username}
          onLogout={guard.requestLogout}
          runningOps={runningOps}
          attention={needsAttention(items)}
          onOpenPalette={() => setPaletteOpen(true)}
          banners={banners.length > 0 ? <div className={styles.banners}>{banners}</div> : undefined}
        >
          <main ref={mainRef} className={styles.main}>
            <Suspense fallback={<Spinner label="Loading page" />}>
              <Routes>
                {ROUTES.map(({ path, screen: Screen }) => (
                  <Route key={path} path={path} element={<Screen />} />
                ))}
              </Routes>
            </Suspense>
          </main>
        </Shell>
      </div>
      <ConfirmDialog
        open={guard.pendingHref !== undefined || guard.pendingLogout}
        title="Discard unsaved changes?"
        message="You have unsaved changes in the editor. Leaving now discards them."
        confirmLabel="Discard changes"
        danger
        onConfirm={guard.leave}
        onCancel={guard.cancel}
      />
      <PaletteHost open={paletteOpen} onClose={() => setPaletteOpen(false)} />
    </>
  );
}
