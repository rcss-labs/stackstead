import { act, cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { StrictMode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { installFakeStream, type FakeStream } from '../../vitest-setup';
import { LogConsole, type LogConsoleProps } from './LogConsole';

const PATH = '/api/stacks/web/logs';
const C_A = 'aaaaaaaaaaaa';
let fake: FakeStream;
const tick = (ms = 50) => act(() => vi.advanceTimersByTimeAsync(ms));

function logEvent(container: string, ns: number, ord: number, text: string, service = 'api', stream = 'stdout') {
  const data = JSON.stringify({ service, container, stream, time: '2026-10-14T10:00:00Z', text });
  return `id: ${container}:${ns}:${ord}\nevent: log\ndata: ${data}\n\n`;
}

function storageStub(initial?: string) {
  const setItem = vi.fn();
  return { getItem: vi.fn(() => initial ?? null), setItem, removeItem: vi.fn(), clear: vi.fn(), key: vi.fn(), length: 0 } as unknown as Storage & { setItem: ReturnType<typeof vi.fn> };
}

function mount(over: Partial<LogConsoleProps> = {}) {
  const props: LogConsoleProps = {
    source: { kind: 'stack', name: 'web' },
    services: ['api', 'worker'],
    service: undefined,
    onService: vi.fn(),
    open: true,
    onOpenChange: vi.fn(),
    mobile: false,
    storage: storageStub(),
    ...over,
  };
  return { props, ...render(<LogConsole {...props} />) };
}

const body = () => screen.getByTestId('log-console-body');
const status = () => screen.getByRole('status');

beforeEach(() => vi.useFakeTimers());
afterEach(() => {
  cleanup();
  fake?.restore();
  vi.useRealTimers();
});

describe('LogConsole follow pill', () => {
  const pulsing = () => document.querySelector('[data-dot]')?.hasAttribute('data-pulse');

  it('pulses while following, keeps a still dot while paused', async () => {
    fake = installFakeStream({ [PATH]: [] });
    mount();
    await tick();
    expect(pulsing()).toBe(true);
    fireEvent.click(screen.getByRole('button', { name: 'Following' }));
    expect(document.querySelector('[data-dot]')).not.toBeNull();
    expect(pulsing()).toBe(false);
  });

  it('turns into a disabled Ended pill without a pulse once the stream ends', async () => {
    fake = installFakeStream({ [PATH]: [logEvent(C_A, 1, 1, 'x'), 'event: end\ndata: {}\n\n'] });
    mount();
    await tick();
    expect(screen.queryByRole('button', { name: 'Following' })).toBeNull();
    const pill = screen.getByRole('button', { name: 'Ended' }) as HTMLButtonElement;
    expect(pill.disabled).toBe(true);
    expect(pulsing()).toBe(false);
  });

  it('is also stopped when the logs are gone', async () => {
    fake = installFakeStream({ [PATH]: { responses: [{ status: 404, body: { error: 'gone' } }] } });
    mount();
    await tick();
    expect((screen.getByRole('button', { name: 'Ended' }) as HTMLButtonElement).disabled).toBe(true);
  });
});

describe('LogConsole stream', () => {
  it('opens one stream with follow and tail 200 and aborts it on unmount', async () => {
    fake = installFakeStream({ [PATH]: [] });
    const { unmount } = mount();
    await tick();
    expect(fake.requests).toHaveLength(1);
    expect(fake.requests[0].path).toContain('tail=200');
    expect(fake.requests[0].path).toContain('follow=1');
    expect(fake.requests[0].path).not.toContain('service=');
    const signal = fake.requests[0].signal!;
    unmount();
    expect(signal.aborted).toBe(true);
  });

  it('keeps exactly one live stream under StrictMode and aborts it on unmount', async () => {
    fake = installFakeStream({ [PATH]: [] });
    const { unmount } = render(
      <StrictMode>
        <LogConsole source={{ kind: 'stack', name: 'web' }} services={['api']} service={undefined} onService={vi.fn()} open onOpenChange={vi.fn()} mobile={false} storage={storageStub()} />
      </StrictMode>,
    );
    await tick();
    const live = fake.requests.filter((r) => !r.signal!.aborted);
    expect(live).toHaveLength(1);
    unmount();
    expect(fake.requests.every((r) => r.signal!.aborted)).toBe(true);
  });

  it('service prop preselects the server filter', async () => {
    fake = installFakeStream({ [PATH]: [] });
    mount({ service: 'worker' });
    await tick();
    expect(fake.requests[0].path).toContain('service=worker');
  });

  it('an unknown service falls back to all services', async () => {
    fake = installFakeStream({ [PATH]: [] });
    mount({ service: 'ghost' });
    await tick();
    expect(fake.requests[0].path).not.toContain('service=');
  });

  it('a service prop change restarts the stream with the new filter', async () => {
    fake = installFakeStream({ [PATH]: [] });
    const { props, rerender } = mount();
    await tick();
    rerender(<LogConsole {...props} service="worker" />);
    await tick();
    expect(fake.requests).toHaveLength(2);
    expect(fake.requests[1].path).toContain('service=worker');
    expect(fake.requests[1].headers.get('Last-Event-ID')).toBeNull();
    expect(fake.requests[0].signal!.aborted).toBe(true);
  });

  it('collapsed shows the latest line and keeps the stream', async () => {
    fake = installFakeStream({ [PATH]: [logEvent(C_A, 1, 1, 'first'), logEvent(C_A, 2, 1, 'second line')] });
    mount({ open: false });
    await tick(200);
    // No rows are rendered behind the collapsed drawer; the inline line is the header's.
    expect(screen.queryAllByTestId('log-row')).toHaveLength(0);
    const header = screen.getByTestId('log-console-header');
    expect(within(header).getByText('second line').parentElement!.textContent).toMatch(/^\d\d:\d\d:\d\d {2}api {2}second line$/);
    expect(within(header).queryByText('first')).toBeNull();
    expect(body().style.height).toBe('0px');
    expect(fake.requests[0].signal!.aborted).toBe(false);
  });

  it('the status text reflects the stream', async () => {
    fake = installFakeStream({ [PATH]: [] });
    mount();
    await tick();
    expect(status().textContent).toBe('live');
    expect(status().getAttribute('data-tone')).toBe('live');
    await act(async () => {
      fake.close(PATH);
      await vi.advanceTimersByTimeAsync(10);
    });
    expect(status().textContent).toBe('reconnecting');
    expect(status().getAttribute('data-tone')).toBe('warning');
  });

  it('announces an expired session even while paused', async () => {
    fake = installFakeStream({ [PATH]: { responses: [{ status: 401, body: { error: 'no' } }] } });
    mount();
    fireEvent.click(screen.getByRole('button', { name: 'Following' }));
    await tick(200);
    expect(status().textContent).toBe('session expired - sign in again');
  });
});

describe('LogConsole header', () => {
  it('the chevron toggles through onOpenChange with aria-expanded', async () => {
    fake = installFakeStream({ [PATH]: [] });
    const onOpenChange = vi.fn();
    mount({ onOpenChange });
    const toggle = screen.getByRole('button', { name: 'Logs' });
    expect(toggle.getAttribute('aria-expanded')).toBe('true');
    fireEvent.click(toggle);
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  it('the pill reads Following and Paused after a click', async () => {
    fake = installFakeStream({ [PATH]: [] });
    mount();
    const pill = screen.getByRole('button', { name: 'Following' });
    expect(pill.getAttribute('aria-pressed')).toBe('false');
    expect(pill.querySelector('[data-dot]')).toBeTruthy();
    fireEvent.click(pill);
    const paused = screen.getByRole('button', { name: 'Paused' });
    expect(paused.getAttribute('aria-pressed')).toBe('true');
  });

  it('pausing counts lines that arrive afterwards in the pill', async () => {
    fake = installFakeStream({ [PATH]: [logEvent(C_A, 1, 1, 'one')] });
    mount();
    await tick(200);
    fireEvent.click(screen.getByRole('button', { name: 'Following' }));
    await act(async () => {
      fake.push(PATH, logEvent(C_A, 2, 1, 'two') + logEvent(C_A, 3, 1, 'three'));
      await vi.advanceTimersByTimeAsync(200);
    });
    expect(screen.getByRole('button', { name: 'Paused · 2 new' })).toBeTruthy();
  });

  it('draws an up chevron that the open state rotates, as the design does', () => {
    fake = installFakeStream({ [PATH]: [] });
    mount();
    expect(screen.getByRole('button', { name: 'Logs' }).querySelector('path')!.getAttribute('d')).toBe('M6 15l6-6 6 6');
  });

  it('a service change un-pauses the console', async () => {
    fake = installFakeStream({ [PATH]: [] });
    const { props, rerender } = mount();
    await tick();
    fireEvent.click(screen.getByRole('button', { name: 'Following' }));
    expect(screen.getByRole('button', { name: 'Paused' })).toBeTruthy();
    rerender(<LogConsole {...props} service="worker" />);
    await tick();
    expect(screen.getByRole('button', { name: 'Following' })).toBeTruthy();
  });

  it('has a level control and a search field only while open', () => {
    fake = installFakeStream({ [PATH]: [] });
    const { rerender, props } = mount();
    expect(screen.getByRole('group', { name: 'Level' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Warn+' })).toBeTruthy();
    expect(screen.getByLabelText('Search logs')).toBeTruthy();
    rerender(<LogConsole {...props} open={false} />);
    expect(screen.queryByLabelText('Search logs')).toBeNull();
  });
});

describe('LogConsole height', () => {
  beforeEach(() => vi.spyOn(window, 'innerHeight', 'get').mockReturnValue(800));
  afterEach(() => vi.restoreAllMocks());

  function resize(height: number) {
    vi.spyOn(window, 'innerHeight', 'get').mockReturnValue(height);
    fireEvent(window, new Event('resize'));
  }

  it.each([
    ['300', '300px'],
    ['999', '640px'],
    ['5', '120px'],
    [undefined, '320px'],
    ['invalid', '320px'],
  ])('initial height %s comes from storage, clamped to %s', (stored, expected) => {
    fake = installFakeStream({ [PATH]: [] });
    mount({ storage: storageStub(stored) });
    expect(body().style.height).toBe(expected);
  });

  it('dragging the handle resizes and writes the clamped value once on release', () => {
    fake = installFakeStream({ [PATH]: [] });
    const storage = storageStub('240');
    mount({ storage });
    const handle = screen.getByRole('separator', { name: 'Resize logs' });
    expect(fireEvent.pointerDown(handle, { clientY: 400, pointerId: 1 })).toBe(false); // default prevented: no text selection
    expect(body().hasAttribute('data-dragging')).toBe(true);
    fireEvent.pointerMove(handle, { clientY: 300, pointerId: 1 });
    expect(body().style.height).toBe('340px');
    expect(handle.getAttribute('aria-valuenow')).toBe('340');
    expect(storage.setItem).not.toHaveBeenCalled();
    fireEvent.pointerMove(handle, { clientY: -500, pointerId: 1 });
    expect(handle.getAttribute('aria-valuenow')).toBe('640');
    fireEvent.pointerUp(handle, { clientY: -500, pointerId: 1 });
    expect(storage.setItem).toHaveBeenCalledTimes(1);
    expect(storage.setItem).toHaveBeenCalledWith('stackstead.logsHeight', '640');
    expect(body().hasAttribute('data-dragging')).toBe(false);
  });

  it('rounds fractional drag heights to whole pixels in the value and the store', () => {
    fake = installFakeStream({ [PATH]: [] });
    const storage = storageStub('240');
    mount({ storage });
    const handle = screen.getByRole('separator', { name: 'Resize logs' });
    fireEvent.pointerDown(handle, { clientY: 400, pointerId: 1 });
    fireEvent.pointerMove(handle, { clientY: 350.4, pointerId: 1 });
    expect(handle.getAttribute('aria-valuenow')).toBe('290');
    expect(body().style.height).toBe('290px');
    fireEvent.pointerMove(handle, { clientY: 300.6, pointerId: 1 });
    expect(handle.getAttribute('aria-valuenow')).toBe('339');
    fireEvent.pointerUp(handle, { clientY: 300.6, pointerId: 1 });
    expect(storage.setItem).toHaveBeenCalledWith('stackstead.logsHeight', '339');
  });

  it('exposes separator semantics', () => {
    fake = installFakeStream({ [PATH]: [] });
    mount();
    const handle = screen.getByRole('separator', { name: 'Resize logs' });
    expect(handle.getAttribute('aria-orientation')).toBe('horizontal');
    expect(handle.getAttribute('aria-valuemin')).toBe('120');
    expect(handle.getAttribute('aria-valuemax')).toBe('640');
    expect(handle.getAttribute('aria-valuenow')).toBe('320');
    expect(handle.getAttribute('tabindex')).toBe('0');
  });

  it('keyboard arrows step the height by 20 and commit', () => {
    fake = installFakeStream({ [PATH]: [] });
    const storage = storageStub('240');
    mount({ storage });
    const handle = screen.getByRole('separator', { name: 'Resize logs' });
    fireEvent.keyDown(handle, { key: 'ArrowUp' });
    expect(handle.getAttribute('aria-valuenow')).toBe('260');
    expect(storage.setItem).toHaveBeenLastCalledWith('stackstead.logsHeight', '260');
    fireEvent.keyDown(handle, { key: 'ArrowDown' });
    fireEvent.keyDown(handle, { key: 'ArrowDown' });
    expect(handle.getAttribute('aria-valuenow')).toBe('220');
    expect(storage.setItem).toHaveBeenLastCalledWith('stackstead.logsHeight', '220');
  });

  it('a throwing storage leaves the console usable', () => {
    fake = installFakeStream({ [PATH]: [] });
    const storage = {
      getItem: () => {
        throw new Error('blocked');
      },
      setItem: () => {
        throw new Error('blocked');
      },
    } as unknown as Storage;
    mount({ storage });
    expect(body().style.height).toBe('320px');
    const handle = screen.getByRole('separator', { name: 'Resize logs' });
    fireEvent.keyDown(handle, { key: 'ArrowUp' });
    expect(handle.getAttribute('aria-valuenow')).toBe('340');
  });

  it('automatic height follows tall and shrinking viewports without saving a preference', () => {
    fake = installFakeStream({ [PATH]: [] });
    const storage = storageStub();
    mount({ storage });
    resize(2160);
    expect(body().style.height).toBe('864px');
    expect(screen.getByRole('separator').getAttribute('aria-valuemax')).toBe('1728');
    resize(600);
    expect(body().style.height).toBe('240px');
    expect(storage.setItem).not.toHaveBeenCalled();
  });

  it('preserves a large saved preference through shrinking, mobile and collapsed views', () => {
    fake = installFakeStream({ [PATH]: [] });
    resize(2160);
    const storage = storageStub('1200');
    const { props, rerender } = mount({ storage });
    expect(body().style.height).toBe('1200px');
    resize(600);
    expect(body().style.height).toBe('440px');
    rerender(<LogConsole {...props} mobile />);
    expect(body().style.height).toBe('280px');
    rerender(<LogConsole {...props} open={false} />);
    expect(body().style.height).toBe('0px');
    resize(2160);
    rerender(<LogConsole {...props} />);
    expect(body().style.height).toBe('1200px');
    expect(storage.setItem).not.toHaveBeenCalled();
  });

  it('can resize and persist beyond the former 560px ceiling', () => {
    fake = installFakeStream({ [PATH]: [] });
    resize(2160);
    const storage = storageStub();
    mount({ storage });
    fireEvent.keyDown(screen.getByRole('separator'), { key: 'ArrowUp' });
    expect(body().style.height).toBe('884px');
    expect(storage.setItem).toHaveBeenLastCalledWith('stackstead.logsHeight', '884');
  });

  it('keeps desktop and mobile logs inside a very short viewport', () => {
    fake = installFakeStream({ [PATH]: [] });
    resize(240);
    const { props, rerender } = mount();
    const handle = screen.getByRole('separator');
    expect(handle.getAttribute('aria-valuemin')).toBe('80');
    expect(handle.getAttribute('aria-valuemax')).toBe('80');
    expect(body().style.height).toBe('80px');
    rerender(<LogConsole {...props} mobile />);
    expect(body().style.height).toBe('80px');
  });

  it('no handle while collapsed', () => {
    fake = installFakeStream({ [PATH]: [] });
    mount({ open: false });
    expect(screen.queryByRole('separator')).toBeNull();
  });

  it('mobile is fixed at 280 with no handle', () => {
    fake = installFakeStream({ [PATH]: [] });
    mount({ mobile: true, storage: storageStub('400') });
    expect(screen.queryByRole('separator')).toBeNull();
    expect(body().style.height).toBe('280px');
  });
});
