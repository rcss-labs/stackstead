import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { BackupManifest, BackupRestorePlan, BackupRun, BackupStack } from '$lib/api/backupTypes';
import { toasts } from '$lib/stores/toasts';
import { installFakeFetch, type FakeFetch, type Routes } from '../../vitest-setup';
import { BackupsScreen } from './BackupsScreen';
import { sampleConfig } from './testKit';

const phone = vi.hoisted(() => ({ value: false }));
vi.mock('../hooks/useIsMobile', () => ({ useIsMobile: () => phone.value }));

vi.mock('../detail/OperationPanel', () => ({
  OperationPanel: ({ operationId }: { operationId: string }) => <div>following {operationId}</div>,
}));

let fake: FakeFetch;
afterEach(() => fake.restore());

const hoursAgo = (h: number) => new Date(Date.now() - h * 3_600_000).toISOString();

const run = (over: Partial<BackupRun> = {}): BackupRun => ({
  id: 1,
  startedAt: hoursAgo(11),
  endedAt: hoursAgo(11),
  trigger: 'schedule',
  status: 'success',
  size: 1024,
  destinations: ['local'],
  items: [],
  ...over,
});

const stack = (over: Partial<BackupStack> = {}): BackupStack => ({
  stack: 'web',
  volumes: true,
  images: false,
  stopDuring: false,
  mounts: [
    { kind: 'volume', name: 'web-data', target: '/data', bytes: 10 },
    { kind: 'volume', name: 'web-cache', target: '/cache', bytes: 10 },
  ],
  ...over,
});

function mount(routes: Routes, url = '/backups') {
  fake = installFakeFetch({
    'GET /api/backups/archives': { body: [] },
    'GET /api/peers/self': { body: { name: 'Mine', slug: 'mine' } },
    'GET /api/backups/config': { body: sampleConfig },
    ...routes,
  });
  render(
    <MemoryRouter initialEntries={[url]}>
      <BackupsScreen />
    </MemoryRouter>,
  );
}

describe('backups headline', () => {
  it('reports success with the age of the latest run', async () => {
    mount({ 'GET /api/backups/runs': { body: [run()] } });
    expect(await screen.findByRole('heading', { level: 1, name: 'Last backup succeeded 11h ago' })).toBeTruthy();
  });

  it('reports a failed latest run with its error', async () => {
    mount({ 'GET /api/backups/runs': { body: [run({ status: 'failed', error: 'S3 endpoint timed out' }), run({ id: 0, startedAt: hoursAgo(40) })] } });
    expect(await screen.findByRole('heading', { level: 1, name: 'Last backup failed 11h ago' })).toBeTruthy();
    expect(screen.getAllByText(/S3 endpoint timed out/).length).toBeGreaterThan(0);
  });

  it('reports a partial run by its failed items', async () => {
    const items = [{ kind: 'volume' as const, stack: 'web', name: 'web-data', status: 'failed' as const }];
    mount({ 'GET /api/backups/runs': { body: [run({ status: 'partial', items })] } });
    expect(await screen.findByRole('heading', { level: 1, name: 'Last backup finished with problems 11h ago' })).toBeTruthy();
    expect(screen.getByText(/1 item failed/)).toBeTruthy();
  });

  it('reports a running backup', async () => {
    mount({ 'GET /api/backups/runs': { body: [run({ status: 'running', endedAt: undefined })] } });
    expect(await screen.findByRole('heading', { level: 1, name: 'A backup is running' })).toBeTruthy();
  });

  it('says plainly that no backup has run yet', async () => {
    mount({ 'GET /api/backups/runs': { body: [] } });
    expect(await screen.findByRole('heading', { level: 1, name: 'No backup has run yet' })).toBeTruthy();
  });

  it('does not claim success when the runs cannot be read', async () => {
    mount({ 'GET /api/backups/runs': { status: 500, body: { error: 'boom' } } });
    expect(await screen.findByRole('heading', { level: 1, name: 'Backup status unavailable' })).toBeTruthy();
    expect(screen.queryByText(/succeeded/)).toBeNull();
  });

  it('starts a run from the header', async () => {
    mount({ 'GET /api/backups/runs': { body: [] }, 'POST /api/backups/run': { status: 202, body: { operation: { id: 'op1' } } } });
    await userEvent.click(await screen.findByRole('button', { name: 'Back up now' }));
    await waitFor(() => expect(fake.calls.some((c) => c.method === 'POST' && c.path === '/api/backups/run')).toBe(true));
  });
});

describe('back up now', () => {
  it('toasts the server message when a run is already going', async () => {
    mount({
      'GET /api/backups/runs': { body: [] },
      'POST /api/backups/run': { status: 409, body: { error: 'a backup is already running', code: 'backup_running' } },
    });
    const seen: string[] = [];
    const off = toasts.subscribe((list) => seen.push(...list.map((t) => t.message)));
    await userEvent.click(await screen.findByRole('button', { name: 'Back up now' }));
    await waitFor(() => expect(seen.some((m) => m.includes('already running'))).toBe(true));
    off();
  });
});

describe('backups tabs', () => {
  it('backup schedule edits survive switching tabs', async () => {
    mount({ 'GET /api/backups/runs': { body: [] }, 'GET /api/backups/stacks': { body: [stack()] } }, '/backups?tab=schedule');
    const path = (await screen.findByLabelText('Path')) as HTMLInputElement;
    await userEvent.clear(path);
    await userEvent.type(path, '/mnt/draft');
    await userEvent.type(screen.getByLabelText('Secret access key'), 'half-typed');
    await userEvent.click(screen.getByRole('tab', { name: 'Archives' }));
    expect(screen.queryByRole('textbox', { name: 'Path' })).toBeNull();
    await userEvent.click(screen.getByRole('tab', { name: 'Schedule' }));
    expect((screen.getByLabelText('Path') as HTMLInputElement).value).toBe('/mnt/draft');
    expect((screen.getByLabelText('Secret access key') as HTMLInputElement).value).toBe('half-typed');
  });

  it('offers Archives, Stacks and Schedule and opens the requested one', async () => {
    mount({ 'GET /api/backups/runs': { body: [] }, 'GET /api/backups/stacks': { body: [stack()] } }, '/backups?tab=stacks');
    const tabs = await screen.findAllByRole('tab');
    expect(tabs.map((t) => t.textContent)).toEqual(['Archives', 'Stacks', 'Schedule']);
    expect(screen.getByRole('tab', { name: 'Stacks' }).getAttribute('aria-selected')).toBe('true');
    expect(await screen.findByLabelText('Volumes web')).toBeTruthy();
  });

  it('keeps the old schedule link working', async () => {
    mount({ 'GET /api/backups/runs': { body: [] } }, '/backups?tab=schedule');
    expect(await screen.findByLabelText('Bucket')).toBeTruthy();
  });

  it('falls back to Archives for an unknown tab', async () => {
    mount({ 'GET /api/backups/runs': { body: [] } }, '/backups?tab=nope');
    expect(screen.getByRole('tab', { name: 'Archives' }).getAttribute('aria-selected')).toBe('true');
  });

  it('warns about unencrypted archives in the list', async () => {
    const archive = (name: string, encrypted: boolean) => ({ dest: 'local', name, hostSlug: 'mine', createdAt: hoursAgo(1), size: 1, encrypted });
    mount({
      'GET /api/backups/runs': { body: [] },
      'GET /api/backups/archives': { body: [archive('a.tar.gz', false), archive('b.tar.gz.enc', true)] },
    });
    expect(await screen.findByText('1 archive is not encrypted')).toBeTruthy();
    expect(within(screen.getByText('a.tar.gz').closest('tr')!).getByText('Unencrypted')).toBeTruthy();
  });
});

describe('stack coverage', () => {
  const okItems = (stackName: string, names: string[]) => [
    { kind: 'stack-files' as const, stack: stackName, status: 'ok' as const },
    ...names.map((name) => ({ kind: 'volume' as const, stack: stackName, name, status: 'ok' as const })),
  ];

  it('shows full coverage only when the last run confirms it', async () => {
    mount(
      {
        'GET /api/backups/runs': { body: [run({ items: okItems('web', ['web-data', 'web-cache']) })] },
        'GET /api/backups/stacks': { body: [stack()] },
      },
      '/backups?tab=stacks',
    );
    expect(await screen.findByText(/2 of 2 volumes covered/)).toBeTruthy();
  });

  it('shows partial coverage when settings leave volumes out', async () => {
    mount(
      {
        'GET /api/backups/runs': { body: [run({ items: okItems('web', []) })] },
        'GET /api/backups/stacks': { body: [stack({ volumes: false })] },
      },
      '/backups?tab=stacks',
    );
    expect(await screen.findByText(/0 of 2 volumes covered/)).toBeTruthy();
  });

  it('does not claim coverage while no run confirms it', async () => {
    mount({ 'GET /api/backups/runs': { body: [] }, 'GET /api/backups/stacks': { body: [stack()] } }, '/backups?tab=stacks');
    expect(await screen.findByText(/Not confirmed by a run yet/)).toBeTruthy();
    expect(screen.queryByText(/volumes covered/)).toBeNull();
  });

  it('does not claim coverage when the runs cannot be read', async () => {
    mount(
      { 'GET /api/backups/runs': { status: 500, body: { error: 'boom' } }, 'GET /api/backups/stacks': { body: [stack()] } },
      '/backups?tab=stacks',
    );
    expect(await screen.findByText(/Not confirmed by a run yet/)).toBeTruthy();
  });

  it('flags a stack the last run did not include', async () => {
    mount(
      {
        'GET /api/backups/runs': { body: [run({ items: okItems('other', []) })] },
        'GET /api/backups/stacks': { body: [stack()] },
      },
      '/backups?tab=stacks',
    );
    expect(await screen.findByText(/Not in the last run/)).toBeTruthy();
  });

  it('flags failed items from the last run', async () => {
    const items = [...okItems('web', ['web-data']), { kind: 'volume' as const, stack: 'web', name: 'web-cache', status: 'failed' as const }];
    mount(
      { 'GET /api/backups/runs': { body: [run({ status: 'partial', items })] }, 'GET /api/backups/stacks': { body: [stack()] } },
      '/backups?tab=stacks',
    );
    expect(await screen.findByText(/1 of 2 volumes covered/)).toBeTruthy();
    expect(screen.getByText(/1 item failed in the last run/)).toBeTruthy();
  });
});

describe('shared volumes', () => {
  it('counts a volume mounted by two services once', async () => {
    const shared = stack({
      mounts: [
        { kind: 'volume', name: 'web-data', target: '/data', bytes: 10 },
        { kind: 'volume', name: 'web-data', target: '/backup-copy', bytes: 10 },
      ],
    });
    const items = [
      { kind: 'stack-files' as const, stack: 'web', status: 'ok' as const },
      { kind: 'volume' as const, stack: 'web', name: 'web-data', status: 'ok' as const },
    ];
    mount(
      { 'GET /api/backups/runs': { body: [run({ items })] }, 'GET /api/backups/stacks': { body: [shared] } },
      '/backups?tab=stacks',
    );
    expect(await screen.findByText(/1 of 1 volume covered/)).toBeTruthy();
  });
});

describe('bind mount coverage', () => {
  const mixed = stack({
    mounts: [
      { kind: 'volume', name: 'web-data', target: '/data', bytes: 10 },
      { kind: 'bind', name: '/srv/web/conf', target: '/conf', bytes: -1 },
    ],
  });
  const files = { kind: 'stack-files' as const, stack: 'web', status: 'ok' as const };
  const volume = { kind: 'volume' as const, stack: 'web', name: 'web-data', status: 'ok' as const };

  it('counts only named volumes in the volumes figure and says binds are not confirmed', async () => {
    mount(
      { 'GET /api/backups/runs': { body: [run({ items: [files, volume] })] }, 'GET /api/backups/stacks': { body: [mixed] } },
      '/backups?tab=stacks',
    );
    expect(await screen.findByText(/1 of 1 volume covered/)).toBeTruthy();
    expect(screen.getByText(/1 bind mount not confirmed/)).toBeTruthy();
    expect(screen.queryByText('Covered')).toBeNull();
  });

  it('counts a bind as copied when the run has an ok item for it', async () => {
    const bind = { kind: 'volume' as const, stack: 'web', name: '/srv/web/conf', status: 'ok' as const };
    mount(
      { 'GET /api/backups/runs': { body: [run({ items: [files, volume, bind] })] }, 'GET /api/backups/stacks': { body: [mixed] } },
      '/backups?tab=stacks',
    );
    expect(await screen.findByText(/1 of 1 bind mount copied/)).toBeTruthy();
    expect(screen.getByText('Covered')).toBeTruthy();
  });

  it('reports a bind the run skipped as a gap', async () => {
    const skipped = { kind: 'volume' as const, stack: 'web', name: '/srv/web/conf', status: 'skipped' as const, note: 'file bind outside the stack directory is not backed up' };
    mount(
      { 'GET /api/backups/runs': { body: [run({ items: [files, volume, skipped] })] }, 'GET /api/backups/stacks': { body: [mixed] } },
      '/backups?tab=stacks',
    );
    expect(await screen.findByText(/1 bind mount not backed up/)).toBeTruthy();
    expect(screen.getByText('Partly covered')).toBeTruthy();
  });
});

describe('test connection', () => {
  const s3On = { ...sampleConfig, s3: { ...sampleConfig.s3, enabled: true } };

  it('reports success', async () => {
    mount({ 'GET /api/backups/runs': { body: [] }, 'GET /api/backups/config': { body: s3On }, 'POST /api/backups/s3/test': { status: 204 } }, '/backups?tab=schedule');
    await userEvent.click(await screen.findByRole('button', { name: 'Test connection' }));
    expect(await screen.findByText('Connected: wrote and removed a test object.')).toBeTruthy();
  });

  it('reports failure with the server message', async () => {
    mount(
      { 'GET /api/backups/runs': { body: [] }, 'GET /api/backups/config': { body: s3On }, 'POST /api/backups/s3/test': { status: 502, body: { error: 'access denied' } } },
      '/backups?tab=schedule',
    );
    await userEvent.click(await screen.findByRole('button', { name: 'Test connection' }));
    expect(await screen.findByText(/Failed: access denied/)).toBeTruthy();
  });
});

describe('restore steps', () => {
  const manifest: BackupManifest = {
    format: 1, stacksteadVersion: '1', host: 'Mine', hostSlug: 'mine', instanceId: 'i', createdAt: hoursAgo(1), status: 'success', networks: 0, items: [], encrypted: false, keyId: '',
    stacks: [{ name: 'web', composePath: '/s/web/compose.yaml', origin: 'managed', project: 'web', wasRunning: true, networks: [], volumes: [], images: [], liveCopy: false }],
  };
  const plan: BackupRestorePlan = {
    stack: 'web', targetName: 'web', targetDir: '/s/web', composePath: '/s/web/compose.yaml', project: 'web', origin: 'managed', overwrites: false,
    fileDiffs: [], volumes: [], images: [], missingNetworks: [], crossHost: false, warnings: [], encrypted: true, keyId: 'k', foreign: false, risks: [],
  };

  async function openRestore() {
    mount({
      'GET /api/backups/runs': { body: [] },
      'GET /api/backups/archives': { body: [{ dest: 'local', name: 'a1.tar.gz', hostSlug: 'mine', createdAt: hoursAgo(1), size: 1, encrypted: true }] },
      'GET /api/backups/archives/local/a1.tar.gz/manifest': { body: manifest },
      'POST /api/backups/restore/plan': { body: plan },
      'POST /api/backups/restore': { status: 202, body: { operation: { id: 'op9' } } },
    });
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Restore' }));
    await screen.findByRole('combobox', { name: 'Stack' });
    return user;
  }

  it('labels three steps and moves through them', async () => {
    const user = await openRestore();
    const steps = () => screen.getByRole('list', { name: 'Restore steps' });
    expect(within(steps()).getByText('Choose').closest('li')!.getAttribute('aria-current')).toBe('step');
    await user.click(screen.getByRole('button', { name: 'Preview restore' }));
    await screen.findByRole('region', { name: 'Restore preview' });
    expect(within(steps()).getByText('Preview').closest('li')!.getAttribute('aria-current')).toBe('step');
    await user.click(screen.getByRole('button', { name: 'Restore stack…' }));
    await user.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Restore' }));
    await screen.findByText('following op9');
    expect(within(steps()).getByText('Restore').closest('li')!.getAttribute('aria-current')).toBe('step');
  });

  it('Back returns to the choices with the entries kept', async () => {
    const user = await openRestore();
    await user.click(screen.getByRole('checkbox', { name: 'Images' }));
    await user.click(screen.getByRole('button', { name: 'Preview restore' }));
    await screen.findByRole('region', { name: 'Restore preview' });
    expect(screen.queryByRole('checkbox', { name: 'Images' })).toBeNull();
    await user.click(screen.getByRole('button', { name: 'Back' }));
    expect((screen.getByRole('checkbox', { name: 'Images' }) as HTMLInputElement).checked).toBe(true);
    expect(screen.queryByRole('region', { name: 'Restore preview' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Restore stack…' })).toBeNull();
  });
});

describe('mobile archives', () => {
  it('mobile archives render as a list', async () => {
    phone.value = true;
    try {
      mount({
        'GET /api/backups/runs': { body: [] },
        'GET /api/backups/archives': { body: [{ dest: 'local', name: 'a1.tar.gz', hostSlug: 'mine', createdAt: hoursAgo(1), size: 1024, encrypted: false }] },
      });
      const list = await screen.findByRole('list', { name: 'Archives of mine' });
      expect(within(list).getByText('a1.tar.gz')).toBeTruthy();
      expect(within(list).getByText(/Unencrypted/)).toBeTruthy();
      expect(screen.queryByRole('table', { name: /Archives/ })).toBeNull();
      expect(within(list).getByRole('button', { name: 'Restore a1.tar.gz' })).toBeTruthy();
    } finally {
      phone.value = false;
    }
  });
});
