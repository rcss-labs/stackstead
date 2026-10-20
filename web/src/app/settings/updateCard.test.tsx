import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { StrictMode } from 'react';
import { afterEach, describe, expect, it } from 'vitest';
import type { UpdateStatus } from '$lib/api/types';
import { installFakeFetch, type FakeFetch } from '../../vitest-setup';
import { UpdateCard } from './UpdateCard';

let fake: FakeFetch;
afterEach(() => {
  cleanup();
  fake?.restore();
});

const STATUS = 'GET /api/settings/update';
const CHECK = 'POST /api/settings/update/check';
const COMMAND = 'sudo /usr/bin/stackstead upgrade';
const hoursAgo = (h: number) => new Date(Date.now() - h * 3600_000).toISOString();

const status = (over: Partial<UpdateStatus> = {}): UpdateStatus => ({
  current: '0.2.0',
  installer: 'deb',
  command: COMMAND,
  state: 'up-to-date',
  latest: '0.2.0',
  notesURL: '',
  checkedAt: hoursAgo(3),
  error: '',
  ...over,
});
const available = (over: Partial<UpdateStatus> = {}) =>
  status({
    state: 'available',
    latest: '0.3.0',
    notesURL: 'https://github.com/rcss-labs/stackstead/releases/tag/v0.3.0',
    ...over,
  });

const command = async () => (await screen.findByTestId('upgrade-command')).textContent;

describe('UpdateCard', () => {
  it('up to date shows the version and checked time, and the command', async () => {
    fake = installFakeFetch({ [STATUS]: { body: status() } });
    render(<UpdateCard />);
    expect(await screen.findByText('up to date')).toBeTruthy();
    expect(screen.getByText('Stackstead v0.2.0 is the latest release.')).toBeTruthy();
    expect(screen.getByText('Checked 3h ago.')).toBeTruthy();
    expect(await command()).toBe(COMMAND);
  });

  it('available shows from→to, the notes link with rel noopener, the command and the restart note', async () => {
    fake = installFakeFetch({ [STATUS]: { body: available() } });
    render(<UpdateCard />);
    expect(await screen.findByText('update available')).toBeTruthy();
    expect(screen.getByText('v0.2.0 → v0.3.0')).toBeTruthy();
    const link = screen.getByRole('link', { name: /release notes/i });
    expect(link.getAttribute('href')).toBe('https://github.com/rcss-labs/stackstead/releases/tag/v0.3.0');
    expect(link.getAttribute('rel')).toContain('noopener');
    expect(await command()).toBe(COMMAND);
    expect(screen.getByText(/The service restarts; this page reconnects by itself/)).toBeTruthy();
  });

  it('available with a non-https notes URL renders no link', async () => {
    fake = installFakeFetch({ [STATUS]: { body: available({ notesURL: 'http://github.com/x' }) } });
    render(<UpdateCard />);
    await screen.findByText('update available');
    expect(screen.queryByRole('link')).toBeNull();
  });

  it('failed shows the sentence and still the command', async () => {
    fake = installFakeFetch({
      [STATUS]: { body: status({ state: 'failed', latest: '', checkedAt: undefined, error: 'network' }) },
    });
    render(<UpdateCard />);
    expect(await screen.findByText('check failed')).toBeTruthy();
    expect(screen.getByText(/no route to github\.com/)).toBeTruthy();
    expect(screen.getByText('You can still upgrade from the terminal.')).toBeTruthy();
    expect(await command()).toBe(COMMAND);
    expect(screen.queryByText(/up to date/i)).toBeNull();
  });

  it('disabled explains update_check and hides Check now', async () => {
    fake = installFakeFetch({ [STATUS]: { body: status({ state: 'disabled', latest: '', checkedAt: undefined }) } });
    render(<UpdateCard />);
    expect(await screen.findByText('checks off')).toBeTruthy();
    expect(screen.getByText(/update_check: false/)).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Check now' })).toBeNull();
    expect(await command()).toBe(COMMAND);
  });

  it('dev build explains and shows the command', async () => {
    fake = installFakeFetch({
      [STATUS]: { body: status({ state: 'unknown', current: 'dev-dirty', latest: '', checkedAt: undefined }) },
    });
    render(<UpdateCard />);
    expect(await screen.findByText('dev build')).toBeTruthy();
    expect(screen.getByText(/development build \(dev-dirty\)/)).toBeTruthy();
    expect(await command()).toBe(COMMAND);
  });

  it('release build before the first check reads Not checked yet', async () => {
    fake = installFakeFetch({ [STATUS]: { body: status({ state: 'unknown', latest: '', checkedAt: undefined }) } });
    render(<UpdateCard />);
    expect(await screen.findByText('Not checked yet.')).toBeTruthy();
    expect(screen.queryByText(/up to date/i)).toBeNull();
  });

  it('an earlier success plus a new failure keeps the result and adds the failure sentence', async () => {
    fake = installFakeFetch({ [STATUS]: { body: available({ error: 'timeout' }) } });
    render(<UpdateCard />);
    expect(await screen.findByText('update available')).toBeTruthy();
    expect(screen.getByText('v0.2.0 → v0.3.0')).toBeTruthy();
    expect(screen.getByText(/the release source did not answer in time/)).toBeTruthy();
    expect(screen.queryByText(/timeout/)).toBeNull();
    expect(await command()).toBe(COMMAND);
  });

  it('Check now posts and re-renders the answer', async () => {
    fake = installFakeFetch({
      [STATUS]: { body: status({ state: 'unknown', latest: '', checkedAt: undefined }) },
      [CHECK]: { body: available() },
    });
    const user = userEvent.setup();
    render(<UpdateCard />);
    await user.click(await screen.findByRole('button', { name: 'Check now' }));
    expect(await screen.findByText('update available')).toBeTruthy();
    expect(fake.calls.filter((c) => c.method === 'POST' && c.path === '/api/settings/update/check')).toHaveLength(1);
    expect(fake.calls.find((c) => c.method === 'POST')?.body).toEqual({});
  });

  it('a failed Check now request keeps the card and shows the message', async () => {
    fake = installFakeFetch({
      [STATUS]: { body: status() },
      [CHECK]: { status: 500, body: { error: 'boom' } },
    });
    const user = userEvent.setup();
    render(<UpdateCard />);
    await user.click(await screen.findByRole('button', { name: 'Check now' }));
    expect(await screen.findByText(/boom/)).toBeTruthy();
    expect(screen.getByText('up to date')).toBeTruthy();
    expect(await command()).toBe(COMMAND);
  });

  it('GET failure shows the message and Retry reloads', async () => {
    let n = 0;
    fake = installFakeFetch({
      [STATUS]: () => (++n === 1 ? { status: 500, body: { error: 'boom' } } : { body: status() }),
    });
    const user = userEvent.setup();
    render(<UpdateCard />);
    expect(await screen.findByText(/boom/)).toBeTruthy();
    expect(screen.queryByTestId('upgrade-command')).toBeNull();
    await user.click(screen.getByRole('button', { name: 'Retry' }));
    expect(await screen.findByText('up to date')).toBeTruthy();
  });

  it('copy button carries exactly the command', async () => {
    fake = installFakeFetch({ [STATUS]: { body: status() } });
    const user = userEvent.setup();
    const written: string[] = [];
    const writeText = (text: string) => {
      written.push(text);
      return Promise.resolve();
    };
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    render(<UpdateCard />);
    await screen.findByTestId('upgrade-command');
    await user.click(screen.getByRole('button', { name: /copy/i }));
    await waitFor(() => expect(written).toEqual([COMMAND]));
  });

  it('loads the status once under StrictMode', async () => {
    fake = installFakeFetch({ [STATUS]: { body: status() } });
    render(
      <StrictMode>
        <UpdateCard />
      </StrictMode>,
    );
    await screen.findByTestId('upgrade-command');
    expect(fake.calls.filter((c) => c.method === 'GET' && c.path === '/api/settings/update')).toHaveLength(1);
  });
});
