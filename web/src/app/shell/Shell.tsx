import { Suspense, lazy } from 'react';
import type { ReactNode } from 'react';
import { useLocation } from 'react-router';
import type { Operation } from '$lib/api/types';
import { splitServerPath } from '$lib/router';
import { stacks } from '$lib/stores/stacks';
import { ErrorBoundary } from '../components/ErrorBoundary';
import { useIsMobile } from '../hooks/useIsMobile';
import { useStore } from '../hooks/useStore';
import { ServerContext, localScope } from '../servers/ServerContext';
import { noScope, viewedScope } from '../servers/viewedServer';
import { pageBack, pageTitle } from './nav';
import styles from './Shell.module.css';

// Each layout's chrome is its own chunk, so the entry carries neither. The one the viewport needs starts loading as this module
// evaluates, alongside the session check, and the stand-ins below hold its space until it arrives.
const loadDesktopChrome = () => import('./desktopChrome');
const loadMobileChrome = () => import('./mobileChrome');
const Sidebar = lazy(() => loadDesktopChrome().then((m) => ({ default: m.Sidebar })));
const TopBar = lazy(() => loadDesktopChrome().then((m) => ({ default: m.TopBar })));
const MobileTopBar = lazy(() => loadMobileChrome().then((m) => ({ default: m.MobileTopBar })));
const TabBar = lazy(() => loadMobileChrome().then((m) => ({ default: m.TabBar })));
if (typeof globalThis.matchMedia === 'function') {
  void (globalThis.matchMedia('(max-width: 759px)').matches ? loadMobileChrome() : loadDesktopChrome()).catch(() => {});
}

/** A chrome piece whose chunk could not be fetched (an upgrade changed the asset hashes under an open tab) leaves a way to reload. */
function Chrome({ standIn, children }: { standIn: string; children: ReactNode }) {
  return (
    <ErrorBoundary
      fallback={
        <button type="button" className={standIn} onClick={() => window.location.reload()}>
          Reload
        </button>
      }
    >
      <Suspense fallback={<div className={standIn} />}>{children}</Suspense>
    </ErrorBoundary>
  );
}

export interface ShellProps {
  username?: string;
  onLogout: () => void;
  runningOps: Operation[];
  attention: boolean;
  onOpenPalette: () => void;
  /** Follows a navigation from the shell; the app passes its unsaved-changes guard. */
  onNavigate?: (href: string) => void;
  banners?: ReactNode;
  /** The page; App supplies the single main landmark. */
  children: ReactNode;
}

export function Shell({ username, onLogout, runningOps, attention, onOpenPalette, onNavigate, banners, children }: ShellProps) {
  const { pathname } = useLocation();
  const mobile = useIsMobile();
  const { items } = useStore(stacks);
  // The top bar and host block describe the server being viewed, through the scope its screens already use.
  const viewed = useStore(viewedScope);
  const { slug } = splitServerPath(pathname);
  const scope = slug === null ? localScope : viewed?.slug === slug ? viewed : noScope;
  const viewedOps = useStore(scope.runningOps);

  // One tree for both layouts, with children at fixed positions, so the routed page keeps its identity (and state) when the viewport crosses 760px.
  const { title, mono } = pageTitle(pathname);
  const topBarOps = slug === null ? runningOps : viewedOps;
  return (
    <ServerContext.Provider value={scope}>
      <div className={mobile ? styles.mobile : styles.desktop}>
        {mobile ? (
          <Chrome standIn={styles.mobileBarSlot}>
            <MobileTopBar title={title} mono={mono} attention={attention} back={pageBack(pathname)} onNavigate={onNavigate} />
          </Chrome>
        ) : (
          <Chrome standIn={styles.sidebarSlot}>
            <Sidebar
              username={username}
              onLogout={onLogout}
              current={pathname}
              stackCount={items.length}
              attention={attention}
              onOpenPalette={onOpenPalette}
              onNavigate={onNavigate}
            />
          </Chrome>
        )}
        <div className={styles.column}>
          {mobile ? null : (
            <Chrome standIn={styles.topBarSlot}>
              <TopBar path={pathname} runningOps={topBarOps} />
            </Chrome>
          )}
          {banners}
          <div className={styles.content}>{children}</div>
        </div>
        {mobile ? (
          <Chrome standIn={styles.tabBarSlot}>
            <TabBar current={pathname} attention={attention} onOpenPalette={onOpenPalette} />
          </Chrome>
        ) : null}
      </div>
    </ServerContext.Provider>
  );
}
