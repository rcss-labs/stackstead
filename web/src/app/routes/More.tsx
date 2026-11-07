import { Link, Navigate, useSearchParams } from 'react-router';
import { ICONS } from '$lib/shell/icons';
import { connectedPeers, peers } from '$lib/servers/peers';
import { useIsMobile } from '../hooks/useIsMobile';
import { useStore } from '../hooks/useStore';
import { CONNECT_SERVER_HREF, SETTINGS, serverNav, type NavEntry } from '../shell/nav';
import styles from './More.module.css';

interface Row {
  href: string;
  label: string;
  icon: keyof typeof ICONS;
}

const row = (entry: NavEntry): Row => ({ href: entry.href, label: entry.label, icon: entry.icon });

/** Phone-only page behind the More tab: every destination the other four tabs do not reach. App owns the main landmark. */
export default function More() {
  const mobile = useIsMobile();
  const [params] = useSearchParams();
  const hasPeers = connectedPeers(useStore(peers).items).length > 0;
  // The tab bar passes the server being viewed, so Images, Storage and the rest stay that server's.
  const slug = params.get('server');
  if (!mobile) return <Navigate replace to="/" />;

  const entries = serverNav(slug);
  const section = (group: NavEntry['group']) => entries.filter((e) => e.group === group).map(row);
  const sections: Array<{ title: string; rows: Row[] }> = [
    { title: 'Resources', rows: [...section('resources'), ...section('more')] },
    { title: 'Add', rows: section('create') },
    {
      title: 'Servers and settings',
      rows: [
        ...(hasPeers ? [{ href: '/all', label: 'All servers', icon: 'grid' as const }] : []),
        { href: CONNECT_SERVER_HREF, label: 'Connected servers', icon: 'server' },
        row(SETTINGS),
      ],
    },
  ];

  return (
    <div className={styles.page}>
      {sections.map(({ title, rows }) => (
        <section key={title} className={styles.section} aria-label={title}>
          <h2 className={styles.heading}>{title}</h2>
          <div className={styles.list}>
            {rows.map(({ href, label, icon }) => (
              <Link key={href} to={href} className={styles.row}>
                <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                  <path d={ICONS[icon]} />
                </svg>
                <span className={styles.label}>{label}</span>
                <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                  <path d="M9 6l6 6-6 6" />
                </svg>
              </Link>
            ))}
          </div>
        </section>
      ))}
    </div>
  );
}
