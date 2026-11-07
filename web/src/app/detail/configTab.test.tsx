import { cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { RawCompose } from '$lib/api/types';
import { appService } from './detailTestKit';
import { ConfigTab } from './ConfigTab';

const file: RawCompose = {
  hash: 'h',
  content: 'name: web\nservices:\n  app:\n    # the web app\n    image: nginx:1\n  db:\n    image: postgres:16\n',
};

function setup(over: Partial<Parameters<typeof ConfigTab>[0]> = {}) {
  const onRetry = vi.fn();
  const onEditYaml = vi.fn();
  render(<ConfigTab service={appService()} compose={file} error="" onRetry={onRetry} onEditYaml={onEditYaml} {...over} />);
  return { onRetry, onEditYaml };
}

afterEach(cleanup);

describe('ConfigTab', () => {
  it('shows the service block sliced from the file', () => {
    setup();
    expect(document.querySelector('pre')!.textContent).toBe('app:\n  # the web app\n  image: nginx:1');
  });

  it('falls back to the formatted config with a note when the block is missing', () => {
    setup({ service: appService({ name: 'cache', config: { image: 'redis:7', limits: {} } }) });
    expect(document.querySelector('pre')!.textContent).toContain('cache:\n  image: redis:7');
    expect(screen.getByText("Shown from the parsed configuration; the file's own text could not be isolated.")).toBeTruthy();
  });

  it('shows a spinner then Retry on error', async () => {
    const view = render(<ConfigTab service={appService()} compose={undefined} error="" onRetry={() => {}} onEditYaml={() => {}} />);
    expect(screen.getByRole('status').textContent).toContain('Loading');
    const onRetry = vi.fn();
    view.rerender(<ConfigTab service={appService()} compose={undefined} error="disk on fire" onRetry={onRetry} onEditYaml={() => {}} />);
    expect(screen.getByText('disk on fire')).toBeTruthy();
    await userEvent.setup().click(screen.getByRole('button', { name: 'Retry' }));
    expect(onRetry).toHaveBeenCalledTimes(1);
  });

  it('Edit YAML calls onEditYaml', async () => {
    const { onEditYaml } = setup();
    await userEvent.setup().click(screen.getByRole('button', { name: 'Edit YAML' }));
    expect(onEditYaml).toHaveBeenCalledTimes(1);
  });

  describe('Used by', () => {
    const deps: RawCompose = {
      hash: 'h',
      content: [
        'services:',
        '  app:',
        '    image: nginx:1',
        '    depends_on:',
        '      db:',
        '        condition: service_healthy',
        '      cache:',
        '        condition: service_started',
        '  worker:',
        '    image: w:1',
        '    depends_on: [db]',
        '  db:',
        '    image: postgres:16',
        '  cache:',
        '    image: redis:7',
        '    depends_on:',
        '      - db',
        '',
      ].join('\n'),
    };
    const names = ['app', 'worker', 'db', 'cache'];
    const row = (term: string) => screen.getByText(term, { selector: 'dt' }).nextElementSibling!.textContent;

    it('Config shows Needed by from depends_on', () => {
      setup({ compose: deps, service: appService({ name: 'db' }), services: names });
      expect(row('Needed by')).toBe('app, worker, cache');
      expect(row('Depends on')).toBe('Nothing');
    });

    it('lists what a service depends on in all three compose spellings', () => {
      setup({ compose: deps, service: appService({ name: 'app' }), services: names });
      expect(row('Depends on')).toBe('db, cache');
      expect(row('Needed by')).toBe('Nothing');
      cleanup();
      setup({ compose: deps, service: appService({ name: 'worker' }), services: names });
      expect(row('Depends on')).toBe('db');
      cleanup();
      setup({ compose: deps, service: appService({ name: 'cache' }), services: names });
      expect(row('Depends on')).toBe('db');
      expect(row('Needed by')).toBe('app');
    });

    it('names the networks of the service, or the default network', () => {
      setup({ compose: deps, service: appService({ config: { image: 'x', limits: {}, networks: [{ name: 'front' }, { name: 'back' }] } }), services: names });
      expect(row('Networks')).toBe('front, back');
      cleanup();
      setup({ compose: deps, service: appService({ name: 'db' }), services: names });
      expect(row('Networks')).toBe('Default');
    });

    it('is omitted until the file has loaded', () => {
      setup({ compose: undefined, services: names });
      expect(screen.queryByText('Needed by')).toBeNull();
    });
  });
});
