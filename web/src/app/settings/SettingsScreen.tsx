import { useLayoutEffect, type MouseEvent } from 'react';
import { useLocation } from 'react-router';
import { logout, session } from '$lib/api/session';
import { density, setDensity, setTheme, theme, type ThemeChoice } from '$lib/stores/appearance';
import { toasts } from '$lib/stores/toasts';
import { Button } from '../components/Button';
import { PanelLabel } from '../components/PanelLabel';
import { Segmented } from '../components/Segmented';
import { useStore } from '../hooks/useStore';
import { ConnectedServersCard } from './ConnectedServersCard';
import { MetricsTokenCard } from './MetricsTokenCard';
import { PasswordForm } from './PasswordForm';
import { RegistriesCard } from './RegistriesCard';
import { SettingsCard } from './SettingsCard';
import styles from './SettingsScreen.module.css';
import { SystemStatusCard } from './SystemStatusCard';
import { UpdateCard } from './UpdateCard';

export const SECTIONS: readonly { id: string; label: string }[] = [
  { id: 'settings-system', label: 'System' },
  { id: 'settings-updates', label: 'Updates' },
  { id: 'settings-prometheus', label: 'Prometheus' },
  { id: 'settings-servers', label: 'Connected servers' },
  { id: 'settings-registries', label: 'Registries' },
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

/**
 * Scrolls to a section card and moves focus to its heading. `behavior: 'auto'` follows the page's own
 * scroll-behavior (instant under reduced motion); scroll-margin-top on the target keeps the heading
 * below the sticky bar.
 */
function scrollToSection(sectionId: string): boolean {
  const target = document.getElementById(sectionId);
  if (!target) return false;
  target.scrollIntoView({ behavior: 'auto' });
  target.querySelector<HTMLElement>('h2')?.focus({ preventScroll: true });
  return true;
}

/**
 * Section links scroll to a card and move focus to its heading. The default fragment navigation would
 * blur the heading again (the target div is not focusable), so scroll, focus and the hash are done here.
 */
function goToSection(event: MouseEvent<HTMLAnchorElement>, sectionId: string) {
  // Modified and non-primary clicks open the section in a new tab or window; leave those to the browser.
  if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
  if (!scrollToSection(sectionId)) return;
  event.preventDefault();
  window.history.replaceState(window.history.state, '', `#${sectionId}`);
}

export function SettingsScreen() {
  const { username } = useStore(session);
  const themeValue = useStore(theme);
  const densityValue = useStore(density);
  const { hash } = useLocation();

  // The cards mount after the browser's own fragment scroll has run, so a /settings#section link
  // (bookmark or in-app navigation) is honoured here once the target exists.
  useLayoutEffect(() => {
    const sectionId = hash.slice(1);
    if (SECTIONS.some((section) => section.id === sectionId)) scrollToSection(sectionId);
  }, [hash]);

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
            <a key={section.id} href={`#${section.id}`} onClick={(event) => goToSection(event, section.id)}>
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
        <div id="settings-servers">
          <ConnectedServersCard />
        </div>
        <div id="settings-registries">
          <RegistriesCard />
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
