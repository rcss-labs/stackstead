import type { ReactNode } from 'react';
import { useLocation } from 'react-router';
import type { Operation } from '$lib/api/types';
import { stacks } from '$lib/stores/stacks';
import { useIsMobile } from '../hooks/useIsMobile';
import { useStore } from '../hooks/useStore';
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
  banners?: ReactNode;
  /** The page; App supplies the single main landmark. */
  children: ReactNode;
}

export function Shell({ username, onLogout, runningOps, attention, onOpenPalette, banners, children }: ShellProps) {
  const { pathname } = useLocation();
  const mobile = useIsMobile();
  const { items } = useStore(stacks);

  // One tree for both layouts, with children at fixed positions, so the routed page keeps its identity (and state) when the viewport crosses 760px.
  const { title, mono } = pageTitle(pathname);
  return (
    <div className={mobile ? styles.mobile : styles.desktop}>
      {mobile ? (
        <MobileTopBar title={title} mono={mono} attention={attention} onOpenPalette={onOpenPalette} />
      ) : (
        <Sidebar username={username} onLogout={onLogout} current={pathname} stackCount={items.length} attention={attention} onOpenPalette={onOpenPalette} />
      )}
      <div className={styles.column}>
        {mobile ? null : <TopBar path={pathname} runningOps={runningOps} />}
        {banners}
        <div className={styles.content}>{children}</div>
      </div>
      {mobile ? <TabBar current={pathname} attention={attention} /> : null}
    </div>
  );
}
