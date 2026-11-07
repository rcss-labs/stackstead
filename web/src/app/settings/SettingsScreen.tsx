import { useState } from 'react';
import { Navigate, NavLink, useLocation, useParams } from 'react-router';
import { logout, session } from '$lib/api/session';
import { density, setDensity, setTheme, theme, type ThemeChoice } from '$lib/stores/appearance';
import { toasts } from '$lib/stores/toasts';
import { Button } from '../components/Button';
import { PanelLabel } from '../components/PanelLabel';
import { Segmented } from '../components/Segmented';
import { useStore } from '../hooks/useStore';
import { BackupSummaryCard } from './BackupSummaryCard';
import { ConnectedServersCard } from './ConnectedServersCard';
import { GitHubCard } from './GitHubCard';
import { ImageDefaultsCard } from './ImageDefaultsCard';
import { MetricsTokenCard } from './MetricsTokenCard';
import { PasswordForm } from './PasswordForm';
import { RegistriesCard } from './RegistriesCard';
import { SettingsCard } from './SettingsCard';
import styles from './SettingsScreen.module.css';
import { DiagnosticsCard } from './DiagnosticsCard';
import { SystemStatusCard } from './SystemStatusCard';
import { UpdateCard } from './UpdateCard';

export const SECTIONS = [
  { id: 'system', label: 'System' },
  { id: 'updates', label: 'Updates' },
  { id: 'prometheus', label: 'Prometheus' },
  { id: 'servers', label: 'Connected servers' },
  { id: 'registries', label: 'Registries' },
  { id: 'github', label: 'GitHub' },
  { id: 'images', label: 'Images' },
  { id: 'backups', label: 'Backups' },
  { id: 'appearance', label: 'Appearance' },
  { id: 'account', label: 'Account' },
] as const;

type SectionId = (typeof SECTIONS)[number]['id'];
const isSectionId = (value: string | undefined): value is SectionId => SECTIONS.some((section) => section.id === value);

const THEME_OPTIONS = [
  { value: 'dark', label: 'Dark' },
  { value: 'light', label: 'Light' },
  { value: 'system', label: 'System' },
];
const DENSITY_OPTIONS = [
  { value: 'compact', label: 'Compact' },
  { value: 'comfortable', label: 'Comfortable' },
];

export function SettingsScreen() {
  const { section } = useParams();
  const { search, hash } = useLocation();
  // Cards hold one-time tokens and unsaved drafts, so a section stays mounted (hidden) once opened.
  // Sections never opened are not mounted, so they load nothing.
  const [opened, setOpened] = useState<readonly string[]>([]);
  if (isSectionId(section) && !opened.includes(section)) setOpened([...opened, section]);

  // Links from the single-page layout looked like /settings#settings-registries.
  const legacy = hash.startsWith('#settings-') ? hash.slice('#settings-'.length) : undefined;
  if (isSectionId(legacy) && legacy !== section) return <Navigate replace to={{ pathname: `/settings/${legacy}`, search }} />;
  if (!isSectionId(section)) return <Navigate replace to={{ pathname: '/settings/system', search }} />;

  return (
    <section className={styles.page} aria-labelledby="settings-heading">
      <div className={styles.side}>
        <h1 id="settings-heading" className={styles.title}>
          Settings
        </h1>
        <nav className={styles.sections} aria-label="Settings sections">
          {SECTIONS.map((item) => (
            <NavLink key={item.id} to={`/settings/${item.id}`}>
              {item.label}
            </NavLink>
          ))}
        </nav>
      </div>
      <div className={styles.cards}>
        {SECTIONS.filter((item) => opened.includes(item.id) || item.id === section).map((item) => (
          <div key={item.id} className={styles.group} hidden={item.id !== section}>
            <SectionBody section={item.id} />
          </div>
        ))}
      </div>
    </section>
  );
}

function SectionBody({ section }: { section: SectionId }) {
  const { username } = useStore(session);
  const themeValue = useStore(theme);
  const densityValue = useStore(density);

  async function doLogout() {
    const { ok } = await logout();
    if (!ok) toasts.push({ kind: 'warning', message: 'Logout may not have completed on the server.' });
  }

  switch (section) {
    case 'system':
      return (
        <>
          <SystemStatusCard />
          <DiagnosticsCard />
        </>
      );
    case 'updates':
      return <UpdateCard />;
    case 'prometheus':
      return <MetricsTokenCard />;
    case 'servers':
      return <ConnectedServersCard />;
    case 'registries':
      return <RegistriesCard />;
    case 'github':
      return <GitHubCard />;
    case 'images':
      return <ImageDefaultsCard />;
    case 'backups':
      return <BackupSummaryCard />;
    case 'appearance':
      return (
        <SettingsCard id="appearance-h" title="Appearance">
          <div className={styles.appearance}>
            <div className={styles.block}>
              <PanelLabel as="span">Theme</PanelLabel>
              <Segmented label="Theme" options={THEME_OPTIONS} value={themeValue} onChange={(value) => setTheme(value as ThemeChoice)} />
            </div>
            <div className={styles.block}>
              <PanelLabel as="span">Density</PanelLabel>
              <Segmented
                label="Density"
                options={DENSITY_OPTIONS}
                value={densityValue}
                onChange={(value) => setDensity(value as 'compact' | 'comfortable')}
              />
            </div>
          </div>
        </SettingsCard>
      );
    case 'account':
      return (
        <SettingsCard id="account-h" title="Account" footer="Remote access and hardening: docs/security.md.">
          <p>
            Signed in as <strong>{username ?? 'unknown'}</strong>. Changing the password signs out other sessions.
          </p>
          <PasswordForm
            actions={
              <Button variant="secondary" onClick={() => void doLogout()}>
                Log out
              </Button>
            }
          />
        </SettingsCard>
      );
  }
}
