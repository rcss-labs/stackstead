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

  if (mobile) {
    const { title, mono } = pageTitle(pathname);
    return (
      <div className={styles.mobile}>
        <MobileTopBar title={title} mono={mono} attention={attention} onOpenPalette={onOpenPalette} />
        {banners}
        <div className={styles.content}>{children}</div>
        <TabBar current={pathname} attention={attention} />
      </div>
    );
  }
  return (
    <div className={styles.desktop}>
      <Sidebar username={username} onLogout={onLogout} current={pathname} stackCount={items.length} attention={attention} onOpenPalette={onOpenPalette} />
      <div className={styles.column}>
        <TopBar path={pathname} runningOps={runningOps} />
        {banners}
        <div className={styles.content}>{children}</div>
      </div>
    </div>
  );
}
