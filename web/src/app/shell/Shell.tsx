import type { ReactNode } from 'react';
import { useLocation } from 'react-router';
import type { Operation } from '$lib/api/types';
import { splitServerPath } from '$lib/router';
import { stacks } from '$lib/stores/stacks';
import { useIsMobile } from '../hooks/useIsMobile';
import { useStore } from '../hooks/useStore';
import { ServerContext, localScope } from '../servers/ServerContext';
import { noScope, viewedScope } from '../servers/viewedServer';
import { MobileTopBar } from './MobileTopBar';
import { Sidebar } from './Sidebar';
import { TabBar } from './TabBar';
import { TopBar } from './TopBar';
import { pageTitle } from './nav';
import styles from './Shell.module.css';

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
          <MobileTopBar title={title} mono={mono} attention={attention} onOpenPalette={onOpenPalette} onNavigate={onNavigate} />
        ) : (
          <Sidebar
            username={username}
            onLogout={onLogout}
            current={pathname}
            stackCount={items.length}
            attention={attention}
            onOpenPalette={onOpenPalette}
            onNavigate={onNavigate}
          />
        )}
        <div className={styles.column}>
          {mobile ? null : <TopBar path={pathname} runningOps={topBarOps} />}
          {banners}
          <div className={styles.content}>{children}</div>
        </div>
        {mobile ? <TabBar current={pathname} attention={attention} /> : null}
      </div>
    </ServerContext.Provider>
  );
}
