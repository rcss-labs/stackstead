import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { Banner } from './Banner';
import { Button } from './Button';
import { Chip } from './Chip';
import { CopyButton } from './CopyButton';
import { EmptyState } from './EmptyState';
import { Led } from './Led';
import { Pager } from './Pager';
import { PanelLabel } from './PanelLabel';
import { RelativeTime } from './RelativeTime';
import { Segmented } from './Segmented';
import { ServiceCells } from './ServiceCells';
import { Sparkline } from './Sparkline';
import { Spinner } from './Spinner';
import { StatusBadge } from './StatusBadge';
import { Tag } from './Tag';

describe('Banner', () => {
  it('uses role=alert for danger and role=status otherwise, with a title', () => {
    const { unmount } = render(
      <Banner kind="danger" title="Docker is down">
        <span>detail</span>
      </Banner>,
    );
    expect(screen.getByRole('alert').textContent).toContain('Docker is down');
    unmount();
    render(
      <Banner kind="info" title="FYI">
        <span>x</span>
      </Banner>,
    );
    expect(screen.getByRole('status').textContent).toContain('FYI');
  });

  it('can be dismissed', async () => {
    render(
      <Banner kind="warning" title="Heads up" dismissible>
        <span>x</span>
      </Banner>,
    );
    await userEvent.setup().click(screen.getByRole('button', { name: 'Dismiss' }));
    expect(screen.queryByText('Heads up')).toBeNull();
  });
});

describe('Button', () => {
  it('fires onClick', async () => {
    const onClick = vi.fn();
    render(
      <Button variant="primary" onClick={onClick}>
        Go
      </Button>,
    );
    await userEvent.setup().click(screen.getByRole('button', { name: 'Go' }));
    expect(onClick).toHaveBeenCalledTimes(1);
  });

  it('keeps the caller className next to its own variant styling', () => {
    render(
      <Button variant="primary" className="x">
        Go
      </Button>,
    );
    const btn = screen.getByRole('button', { name: 'Go' });
    expect(btn.classList.contains('x')).toBe(true);
    expect(btn.classList.length).toBeGreaterThan(1);
  });

  it('exposes disabledReason to AT, stays focusable and does not fire', async () => {
    const user = userEvent.setup();
    const onClick = vi.fn();
    render(
      <Button variant="primary" disabledReason="Compose is unavailable" onClick={onClick}>
        Apply
      </Button>,
    );
    const btn = screen.getByRole('button', { name: 'Apply' });
    expect(btn.getAttribute('aria-disabled')).toBe('true');
    expect((btn as HTMLButtonElement).disabled).toBe(false);
    expect(btn.getAttribute('title')).toBe('Compose is unavailable');
    const describedBy = btn.getAttribute('aria-describedby');
    expect(describedBy).toBeTruthy();
    expect(document.getElementById(describedBy!)?.textContent).toBe('Compose is unavailable');
    await user.tab();
    expect(document.activeElement).toBe(btn);
    await user.click(btn);
    expect(onClick).not.toHaveBeenCalled();
  });

  it('forwards rest props such as aria-label and id', () => {
    render(
      <Button variant="ghost" aria-label="Close panel" id="close-btn">
        ×
      </Button>,
    );
    expect(screen.getByRole('button', { name: 'Close panel' }).id).toBe('close-btn');
  });

  it('is busy and inert while loading, and truly disabled when disabled', async () => {
    const onClick = vi.fn();
    const { rerender } = render(
      <Button variant="secondary" loading onClick={onClick}>
        Save
      </Button>,
    );
    const btn = screen.getByRole('button', { name: /Save/ });
    expect(btn.getAttribute('aria-busy')).toBe('true');
    await userEvent.setup().click(btn);
    expect(onClick).not.toHaveBeenCalled();
    rerender(
      <Button variant="secondary" disabled onClick={onClick}>
        Save
      </Button>,
    );
    expect((screen.getByRole('button', { name: 'Save' }) as HTMLButtonElement).disabled).toBe(true);
  });

  it('applies size and defaults to md', () => {
    render(
      <>
        <Button variant="secondary" size="sm">
          Small
        </Button>
        <Button variant="secondary">Default</Button>
      </>,
    );
    expect(screen.getByRole('button', { name: 'Small' }).getAttribute('data-size')).toBe('sm');
    expect(screen.getByRole('button', { name: 'Default' }).getAttribute('data-size')).toBe('md');
  });

  it('icon button keeps aria-label as its name', () => {
    render(
      <Button variant="ghost" icon aria-label="Refresh">
        <span aria-hidden="true">↻</span>
      </Button>,
    );
    expect(screen.getByRole('button', { name: 'Refresh' }).getAttribute('data-icon')).toBe('true');
  });

  it('danger is outlined by default and filled when confirm', () => {
    render(
      <>
        <Button variant="danger">Delete</Button>
        <Button variant="danger" confirm>
          Remove it
        </Button>
      </>,
    );
    expect(screen.getByRole('button', { name: 'Delete' }).getAttribute('data-confirm')).toBeNull();
    expect(screen.getByRole('button', { name: 'Remove it' }).getAttribute('data-confirm')).toBe('true');
    expect(screen.getByRole('button', { name: 'Delete' }).getAttribute('data-variant')).toBe('danger');
  });
});

describe('StatusBadge', () => {
  it.each([
    ['running', 'running', 'success'],
    ['succeeded', 'succeeded', 'success'],
    ['healthy', 'healthy', 'success'],
    ['partial', 'partial', 'warning'],
    ['timed_out', 'timed out', 'warning'],
    ['starting', 'starting', 'warning'],
    ['restarting', 'restarting', 'warning'],
    ['stopped', 'stopped', 'muted'],
    ['cancelled', 'cancelled', 'muted'],
    ['unknown', 'unknown', 'muted'],
    ['exited', 'exited', 'muted'],
    ['none', 'none', 'muted'],
    ['unavailable', 'unavailable', 'danger'],
    ['failed', 'failed', 'danger'],
    ['unhealthy', 'unhealthy', 'danger'],
  ] as const)('%s renders %s in the %s tone', (status, text, tone) => {
    render(<StatusBadge status={status} />);
    expect(screen.getByText(text).getAttribute('data-tone')).toBe(tone);
  });

  it('renders an unknown state muted with the raw word', () => {
    render(<StatusBadge status="removing" />);
    expect(screen.getByText('removing').getAttribute('data-tone')).toBe('muted');
  });

  it('renders no decorative glyph', () => {
    const { container } = render(<StatusBadge status="running" />);
    expect(container.querySelector('[aria-hidden]')).toBeNull();
    expect(container.textContent).toBe('running');
  });
});

describe('CopyButton', () => {
  it('copies via the clipboard API and announces Copied', async () => {
    const user = userEvent.setup(); // setup() installs its own clipboard stub; override it afterwards
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    render(<CopyButton text="secret-token" label="Copy token" />);
    await user.click(screen.getByRole('button', { name: 'Copy token' }));
    expect(writeText).toHaveBeenCalledWith('secret-token');
    await waitFor(() => expect(screen.getByRole('status').textContent).toContain('Copied'));
  });

  it('falls back to a textarea and execCommand without the clipboard API', async () => {
    const user = userEvent.setup();
    Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true });
    const exec = vi.fn().mockReturnValue(true);
    (document as unknown as { execCommand: unknown }).execCommand = exec;
    render(<CopyButton text="abc" />);
    await user.click(screen.getByRole('button', { name: 'Copy' }));
    expect(exec).toHaveBeenCalledWith('copy');
    await waitFor(() => expect(screen.getByRole('status').textContent).toContain('Copied'));
    expect(document.querySelector('textarea')).toBeNull();
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Copy' }));
  });

  it('says so when both copy paths fail', async () => {
    const user = userEvent.setup();
    Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true });
    (document as unknown as { execCommand: unknown }).execCommand = vi.fn().mockReturnValue(false);
    render(<CopyButton text="abc" />);
    await user.click(screen.getByRole('button', { name: 'Copy' }));
    await waitFor(() => expect(screen.getByRole('status').textContent).toContain('Copy failed'));
  });

  it('re-announces on a repeat copy', async () => {
    const user = userEvent.setup();
    Object.defineProperty(navigator, 'clipboard', { value: { writeText: vi.fn().mockResolvedValue(undefined) }, configurable: true });
    render(<CopyButton text="abc" />);
    const status = screen.getByRole('status');
    await user.click(screen.getByRole('button', { name: 'Copy' }));
    await waitFor(() => expect(status.textContent).toContain('Copied'));
    let mutations = 0;
    const observer = new MutationObserver((records) => (mutations += records.length));
    observer.observe(status, { childList: true, characterData: true, subtree: true });
    await user.click(screen.getByRole('button', { name: 'Copy' }));
    await waitFor(() => expect(mutations).toBeGreaterThan(0));
    observer.disconnect();
  });
});

describe('Spinner and EmptyState', () => {
  it('Spinner announces its label as status', () => {
    render(<Spinner label="Loading stacks" />);
    expect(screen.getByRole('status').textContent).toContain('Loading stacks');
  });

  it('Spinner defaults its label to Loading', () => {
    render(<Spinner />);
    expect(screen.getByRole('status').textContent).toContain('Loading');
  });

  it('EmptyState shows a title, description and action', () => {
    render(
      <EmptyState title="No stacks" description="Create one.">
        <button>Create</button>
      </EmptyState>,
    );
    expect(screen.getByRole('heading', { name: 'No stacks' })).toBeTruthy();
    expect(screen.getByText('Create one.')).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Create' })).toBeTruthy();
  });
});

describe('RelativeTime', () => {
  it('renders relative text with the absolute time as title', () => {
    const iso = new Date(Date.now() - 3 * 60_000).toISOString();
    render(<RelativeTime iso={iso} />);
    const el = screen.getByText('3m ago');
    expect(el.tagName).toBe('TIME');
    expect(el.getAttribute('datetime')).toBe(iso);
    expect(el.getAttribute('title')).toBeTruthy();
  });

  it('shows an em dash for an unparseable timestamp', () => {
    render(<RelativeTime iso="nope" />);
    expect(screen.getByText('—')).toBeTruthy();
  });
});

describe('Led', () => {
  it('is aria-hidden without a label and role=img with one', () => {
    const { container, unmount } = render(<Led tone="success" />);
    expect(container.firstElementChild?.getAttribute('aria-hidden')).toBe('true');
    expect(screen.queryByRole('img')).toBeNull();
    unmount();
    render(<Led tone="danger" label="Unhealthy" />);
    expect(screen.getByRole('img', { name: 'Unhealthy' }).getAttribute('data-tone')).toBe('danger');
  });

  it('sets data-pulse only when pulse', () => {
    const { container } = render(
      <>
        <Led tone="danger" pulse />
        <Led tone="muted" />
      </>,
    );
    const [pulsing, still] = Array.from(container.children);
    expect(pulsing.getAttribute('data-pulse')).toBe('true');
    expect(still.getAttribute('data-pulse')).toBeNull();
  });
});

describe('Chip', () => {
  it('aria-pressed reflects pressed and click calls onClick', async () => {
    const onClick = vi.fn();
    const { rerender } = render(
      <Chip pressed={false} onClick={onClick} count={3}>
        Running
      </Chip>,
    );
    const chip = screen.getByRole('button', { name: /Running/ });
    expect(chip.getAttribute('aria-pressed')).toBe('false');
    await userEvent.setup().click(chip);
    expect(onClick).toHaveBeenCalledTimes(1);
    rerender(
      <Chip pressed onClick={onClick} count={3}>
        Running
      </Chip>,
    );
    expect(screen.getByRole('button', { name: /Running/ }).getAttribute('aria-pressed')).toBe('true');
  });
});

describe('Segmented', () => {
  it('is a labelled group with exactly one pressed button, and reports the chosen value', async () => {
    const onChange = vi.fn();
    render(
      <Segmented
        label="Log level"
        value="all"
        onChange={onChange}
        options={[
          { value: 'all', label: 'All' },
          { value: 'warn', label: 'Warn+' },
          { value: 'error', label: 'Errors' },
        ]}
      />,
    );
    const group = screen.getByRole('group', { name: 'Log level' });
    expect(group.querySelectorAll('[aria-pressed="true"]')).toHaveLength(1);
    expect(screen.getByRole('button', { name: 'All' }).getAttribute('aria-pressed')).toBe('true');
    await userEvent.setup().click(screen.getByRole('button', { name: 'Errors' }));
    expect(onChange).toHaveBeenCalledWith('error');
  });
});

describe('Tag and PanelLabel', () => {
  it('Tag renders its text', () => {
    render(<Tag tone="info">imported</Tag>);
    expect(screen.getByText('imported')).toBeTruthy();
  });

  it('PanelLabel renders its text in the chosen element', () => {
    const { unmount } = render(<PanelLabel>Services</PanelLabel>);
    expect(screen.getByText('Services').tagName).toBe('DIV');
    unmount();
    render(<PanelLabel as="h2" id="svc">Services</PanelLabel>);
    const heading = screen.getByRole('heading', { name: 'Services' });
    expect(heading.tagName).toBe('H2');
    expect(heading.id).toBe('svc');
  });
});

describe('Sparkline', () => {
  it('is aria-hidden; empty values draw the flat path', () => {
    const { container } = render(<Sparkline values={[]} tone="accent" />);
    const svg = container.querySelector('svg')!;
    expect(svg.getAttribute('aria-hidden')).toBe('true');
    expect(container.querySelector('path')!.getAttribute('d')).toBe('M0,17L60,17');
  });

  it('draws a path from the samples', () => {
    const { container } = render(<Sparkline values={[1, 3, 2]} tone="info" />);
    expect(container.querySelector('path')!.getAttribute('d')).toMatch(/^M/);
  });
});

describe('ServiceCells', () => {
  it('renders count cells with the title, hidden from AT', () => {
    const { container } = render(<ServiceCells count={4} good={2} bad={1} starting={0} title="2 of 4 up" />);
    const root = container.firstElementChild!;
    expect(root.getAttribute('aria-hidden')).toBe('true');
    expect(root.getAttribute('title')).toBe('2 of 4 up');
    expect(root.children).toHaveLength(4);
    expect(Array.from(root.children).map((c) => c.getAttribute('data-tone'))).toEqual(['success', 'success', 'danger', 'off']);
  });
});

describe('Pager', () => {
  const base = { from: 1, to: 25, total: 60, label: 'Images' };

  it('is hidden for a single page', () => {
    const { container } = render(<Pager {...base} page={1} pages={1} onPage={() => {}} />);
    expect(container.firstChild).toBeNull();
  });

  it('disables Previous on page 1 and Next calls onPage(page + 1)', async () => {
    const onPage = vi.fn();
    render(<Pager {...base} page={1} pages={3} onPage={onPage} />);
    expect(screen.getByRole('navigation', { name: 'Images pages' })).toBeTruthy();
    expect((screen.getByRole('button', { name: 'Previous' }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText('1–25 of 60')).toBeTruthy();
    await userEvent.setup().click(screen.getByRole('button', { name: 'Next' }));
    expect(onPage).toHaveBeenCalledWith(2);
  });
});
