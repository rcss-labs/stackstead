import { logout, session } from '$lib/api/session';
import { density, setDensity, setTheme, theme, type ThemeChoice } from '$lib/stores/appearance';
import { toasts } from '$lib/stores/toasts';
import { Button } from '../components/Button';
import { PanelLabel } from '../components/PanelLabel';
import { Segmented } from '../components/Segmented';
import { useStore } from '../hooks/useStore';
import { MetricsTokenCard } from './MetricsTokenCard';
import { PasswordForm } from './PasswordForm';
import { SettingsCard } from './SettingsCard';
import styles from './SettingsScreen.module.css';
import { SystemStatusCard } from './SystemStatusCard';
import { UpdateCard } from './UpdateCard';

export const SECTIONS: readonly { id: string; label: string }[] = [
  { id: 'settings-system', label: 'System' },
  { id: 'settings-updates', label: 'Updates' },
  { id: 'settings-prometheus', label: 'Prometheus' },
  { id: 'settings-appearance', label: 'Appearance' },
  { id: 'settings-account', label: 'Account' },
];

const THEME_OPTIONS = [
  { value: 'dark', label: 'Dark' },
  { value: 'light', label: 'Light' },
  { value: 'system', label: 'System' },
];
const DENSITY_OPTIONS = [
  { value: 'compact', label: 'Compact' },
  { value: 'comfortable', label: 'Comfortable' },
];

/** The anchor scrolls to the card; focus follows it so keyboard and screen-reader users land on the card too. */
function focusCardHeading(sectionId: string) {
  document.getElementById(sectionId)?.querySelector<HTMLElement>('h2')?.focus({ preventScroll: true });
}

export function SettingsScreen() {
  const { username } = useStore(session);
  const themeValue = useStore(theme);
  const densityValue = useStore(density);

  async function doLogout() {
    const { ok } = await logout();
    if (!ok) toasts.push({ kind: 'warning', message: 'Logout may not have completed on the server.' });
  }

  return (
    <section className={styles.page} aria-labelledby="settings-heading">
      <div className={styles.side}>
        <h1 id="settings-heading" className={styles.title}>
          Settings
        </h1>
        <nav className={styles.sections} aria-label="Settings sections">
          {SECTIONS.map((section) => (
            <a key={section.id} href={`#${section.id}`} onClick={() => focusCardHeading(section.id)}>
              {section.label}
            </a>
          ))}
        </nav>
      </div>
      <div className={styles.cards}>
        <div id="settings-system">
          <SystemStatusCard />
        </div>
        <div id="settings-updates">
          <UpdateCard />
        </div>
        <div id="settings-prometheus">
          <MetricsTokenCard />
        </div>
        <div id="settings-appearance">
          <SettingsCard id="appearance-h" title="Appearance">
            <div className={styles.appearance}>
              <div className={styles.block}>
                <PanelLabel as="span">Theme</PanelLabel>
                <Segmented
                  label="Theme"
                  options={THEME_OPTIONS}
                  value={themeValue}
                  onChange={(value) => setTheme(value as ThemeChoice)}
                />
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
        </div>
        <div id="settings-account">
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
        </div>
      </div>
    </section>
  );
}
