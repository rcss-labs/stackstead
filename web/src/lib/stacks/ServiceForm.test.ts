import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import type { Field } from '../api/types';
import Host from './ServiceFormHost.test-fixture.svelte';

const HINT = "This field uses YAML the form can't edit — open the raw YAML editor";

describe('ServiceForm', () => {
  it('renders a read-only group uneditable with the reason and a raw-editor button', async () => {
    const onRawEditor = vi.fn();
    render(Host, { initial: { ports: ['80:80'], readOnly: new Set<Field>(['ports']) }, onRawEditor });
    const port = screen.getByLabelText('Port 1');
    expect((port as HTMLInputElement).readOnly || (port as HTMLInputElement).disabled).toBe(true);
    expect(screen.getByText(HINT)).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Add port' })).toBeNull();
    await userEvent.setup().click(screen.getByRole('button', { name: 'Open raw YAML editor' }));
    expect(onRawEditor).toHaveBeenCalledOnce();
  });

  it('keeps typing into a read-only field from changing the form', async () => {
    const { component } = render(Host, { initial: { image: 'nginx', readOnly: new Set<Field>(['image']) } });
    await userEvent.setup().type(screen.getByLabelText('Image'), 'xyz');
    expect(component.getForm().image).toBe('nginx');
  });

  it('shows errors next to their field with aria-invalid and a linked message', () => {
    render(Host, {
      initial: { image: '', ports: ['80:80', 'bad'], env: [{ name: '', value: 'x' }] },
      errors: { image: 'image is required', 'ports[1]': 'invalid port "bad"', 'env[0].name': 'variable name is required' },
    });
    const image = screen.getByLabelText('Image');
    expect(image.getAttribute('aria-invalid')).toBe('true');
    expect(document.getElementById(image.getAttribute('aria-describedby')!.split(' ')[0])!.textContent).toContain('image is required');
    expect(screen.getByLabelText('Port 1').getAttribute('aria-invalid')).toBeNull();
    expect(screen.getByLabelText('Port 2').getAttribute('aria-invalid')).toBe('true');
    expect(screen.getByText(/invalid port "bad"/)).toBeTruthy();
    expect(screen.getByLabelText('Variable 1 name').getAttribute('aria-invalid')).toBe('true');
    expect(screen.getByLabelText('Variable 1 value').getAttribute('aria-invalid')).toBeNull();
  });

  it('shows form-level errors in an alert', () => {
    render(Host, { errors: { _form: 'compose rejected the file' } });
    expect(screen.getByRole('alert').textContent).toContain('compose rejected the file');
  });

  it("hides the other source's errors after switching source", async () => {
    render(Host, { errors: { image: 'image is required' } });
    expect(screen.getByText('image is required')).toBeTruthy();
    await userEvent.setup().click(screen.getByRole('radio', { name: 'From build' }));
    expect(screen.queryByText('image is required')).toBeNull();
    await userEvent.setup().click(screen.getByRole('radio', { name: 'From image' }));
    expect(screen.getByText('image is required')).toBeTruthy();
  });

  it('shows build errors only for the build source and offers Browse when onbrowse is given', async () => {
    const onbrowse = vi.fn((_mode: string, cb: (p: string) => void) => cb('/srv/app'));
    const { component } = render(Host, {
      initial: { source: 'build' },
      errors: { 'build.context': 'build context is required' },
      onbrowse,
    });
    expect(screen.getByLabelText('Build context').getAttribute('aria-invalid')).toBe('true');
    await userEvent.setup().click(screen.getByRole('button', { name: 'Browse for build context' }));
    expect(onbrowse).toHaveBeenCalledWith('dir', expect.any(Function));
    await waitFor(() => expect(component.getForm().buildContext).toBe('/srv/app'));
    await userEvent.setup().click(screen.getByRole('button', { name: 'Browse for Dockerfile' }));
    expect(onbrowse).toHaveBeenLastCalledWith('file', expect.any(Function));
  });

  it('omits Browse buttons without onbrowse', () => {
    render(Host, { initial: { source: 'build' } });
    expect(screen.queryByRole('button', { name: /Browse/ })).toBeNull();
  });

  it('splits pasted KEY=VALUE lines into rows', async () => {
    const user = userEvent.setup();
    const { component } = render(Host, { initial: { env: [{ name: '', value: '' }] } });
    await user.click(screen.getByLabelText('Variable 1 name'));
    await user.paste('A=1\n# comment\nB=x=y\n\nC=');
    await waitFor(() =>
      expect(component.getForm().env).toEqual([
        { name: 'A', value: '1' },
        { name: 'B', value: 'x=y' },
        { name: 'C', value: '' },
      ]),
    );
    expect((screen.getByLabelText('Variable 2 name') as HTMLInputElement).value).toBe('B');
  });

  it('pastes a plain word into the name field unchanged', async () => {
    const user = userEvent.setup();
    const { component } = render(Host, { initial: { env: [{ name: '', value: '' }] } });
    await user.click(screen.getByLabelText('Variable 1 name'));
    await user.paste('HOME');
    expect(component.getForm().env).toEqual([{ name: 'HOME', value: '' }]);
  });

  it('updates the bound form on input and add/remove', async () => {
    const user = userEvent.setup();
    const { component } = render(Host, { initial: { image: 'a' }, showName: true });
    await user.clear(screen.getByLabelText('Image'));
    await user.type(screen.getByLabelText('Image'), 'redis:7');
    await user.type(screen.getByLabelText('Service name'), '2');
    await user.click(screen.getByRole('button', { name: 'Add port' }));
    await user.type(screen.getByLabelText('Port 1'), '6379:6379');
    await user.type(screen.getByLabelText('CPUs'), '0.5');
    await user.selectOptions(screen.getByLabelText('Restart policy'), 'always');
    expect(component.getForm()).toMatchObject({ image: 'redis:7', ports: ['6379:6379'], cpus: '0.5', restart: 'always' });
    expect(component.getName()).toBe('web2');
    await user.click(screen.getByRole('button', { name: 'Remove item 1' }));
    expect(component.getForm().ports).toEqual([]);
  });

  it('keeps a custom restart value such as on-failure:3 selectable', () => {
    render(Host, { initial: { restart: 'on-failure:3' } });
    expect((screen.getByLabelText('Restart policy') as HTMLSelectElement).value).toBe('on-failure:3');
  });

  it('handles network mode presets and custom values', async () => {
    const user = userEvent.setup();
    const { component } = render(Host, { initial: { networkMode: 'service:db' } });
    expect((screen.getByLabelText('Network mode') as HTMLSelectElement).value).toBe('custom');
    expect((screen.getByLabelText('Custom network mode') as HTMLInputElement).value).toBe('service:db');
    await user.selectOptions(screen.getByLabelText('Network mode'), 'host');
    expect(component.getForm().networkMode).toBe('host');
    expect(screen.queryByLabelText('Custom network mode')).toBeNull();
  });

  it('renders healthcheck fields only when enabled', async () => {
    const user = userEvent.setup();
    const { component } = render(Host);
    expect(screen.queryByLabelText('Healthcheck command')).toBeNull();
    await user.click(screen.getByRole('checkbox', { name: 'Enable healthcheck' }));
    await user.type(screen.getByLabelText('Healthcheck command'), 'curl -f localhost');
    expect(component.getForm().healthcheck).toMatchObject({ enabled: true, test: 'curl -f localhost' });
  });

  it('uses the networkPicker snippet or a plain list for networks', async () => {
    const user = userEvent.setup();
    const { unmount } = render(Host, { withPicker: true });
    expect(screen.getByTestId('picker')).toBeTruthy();
    unmount();
    const { component } = render(Host);
    await user.click(screen.getByRole('button', { name: 'Add network' }));
    await user.type(screen.getByLabelText('Network 1 name'), 'backend');
    await user.type(screen.getByLabelText('Network 1 IPv4 address'), '10.0.0.5');
    expect(component.getForm().networks).toEqual([{ name: 'backend', ipv4Address: '10.0.0.5' }]);
  });

  it('focuses the first invalid control, or the form alert, via focusFirstError', async () => {
    const { component, rerender } = render(Host, {
      initial: { image: '', ports: ['x'] },
      errors: { 'ports[0]': 'bad port', image: 'image is required' },
    });
    component.focusFirstError();
    expect(document.activeElement).toBe(screen.getByLabelText('Image'));
    await rerender({ errors: { _form: 'boom' } });
    component.focusFirstError();
    expect(document.activeElement).toBe(screen.getByRole('alert'));
  });
});
