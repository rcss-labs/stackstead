import { useImperativeHandle, useRef, useState } from 'react';
import type { Ref } from 'react';
import { act, cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Field } from '$lib/api/types';
import { emptyForm, type FormErrors, type ServiceForm as Form } from '$lib/compose/form';
import { ServiceForm, type ServiceFormHandle } from './ServiceForm';

afterEach(cleanup);

const HINT = "This field uses YAML the form can't edit — open the raw YAML editor";

interface HostHandle extends ServiceFormHandle {
  getForm(): Form;
  getName(): string;
  setForm(next: Form): void;
}

interface HostProps {
  initial?: Partial<Form>;
  errors?: FormErrors;
  onBrowse?: (mode: 'dir' | 'file', set: (path: string) => void) => void;
  onRawEditor?: () => void;
  withPicker?: boolean;
  showName?: boolean;
  nameReadonly?: boolean;
  idPrefix?: string;
  handle?: Ref<HostHandle>;
}

// Owns the form as state, like a real parent (edit / add / New stack) would.
function Host({ initial, errors = {}, withPicker, handle, ...rest }: HostProps) {
  const [form, setForm] = useState<Form>({ ...emptyForm(), ...initial });
  const [name, setName] = useState('web');
  const latest = useRef({ form, name });
  latest.current = { form, name };
  const inner = useRef<ServiceFormHandle>(null);
  useImperativeHandle(handle, () => ({
    getForm: () => latest.current.form,
    getName: () => latest.current.name,
    setForm,
    focusFirstError: () => inner.current?.focusFirstError(),
  }));
  return (
    <ServiceForm
      ref={inner}
      form={form}
      onChange={setForm}
      errors={errors}
      name={name}
      onNameChange={setName}
      networkPicker={
        withPicker
          ? (f, rowErrors) => (
              <div data-testid="picker">
                {f.networks.length} networks {JSON.stringify(rowErrors)}
              </div>
            )
          : undefined
      }
      {...rest}
    />
  );
}

function setup(props: HostProps = {}) {
  const handle = { current: null } as { current: HostHandle | null };
  const utils = render(<Host {...props} handle={handle as Ref<HostHandle>} />);
  return {
    handle: handle as { current: HostHandle },
    rerender: (next: HostProps) => utils.rerender(<Host {...props} {...next} handle={handle as Ref<HostHandle>} />),
  };
}

describe('ServiceForm', () => {
  it('renders a read-only group uneditable with the reason and a raw-editor button', async () => {
    const onRawEditor = vi.fn();
    setup({ initial: { ports: ['80:80'], readOnly: new Set<Field>(['ports']) }, onRawEditor });
    const port = screen.getByLabelText('Port 1') as HTMLInputElement;
    expect(port.readOnly || port.disabled).toBe(true);
    expect(screen.getByText(HINT)).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Add port' })).toBeNull();
    await userEvent.setup().click(screen.getByRole('button', { name: 'Open raw YAML editor for ports' }));
    expect(onRawEditor).toHaveBeenCalledOnce();
  });

  it('keeps typing into a read-only field from changing the form', async () => {
    const { handle } = setup({ initial: { image: 'nginx', readOnly: new Set<Field>(['image']) } });
    await userEvent.setup().type(screen.getByLabelText('Image'), 'xyz');
    expect(handle.current.getForm().image).toBe('nginx');
  });

  it('shows errors next to their field with aria-invalid and a linked message', () => {
    setup({
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
    setup({ errors: { _form: 'compose rejected the file' } });
    expect(screen.getByRole('alert').textContent).toContain('compose rejected the file');
  });

  it("hides the build source's errors while the image source is selected", async () => {
    setup({ errors: { 'build.context': 'build context is required' } });
    expect(screen.queryByText('build context is required')).toBeNull();
    await userEvent.setup().click(screen.getByRole('radio', { name: 'From Dockerfile' }));
    expect(screen.getByText('build context is required')).toBeTruthy();
    await userEvent.setup().click(screen.getByRole('radio', { name: 'From Image' }));
    expect(screen.queryByText('build context is required')).toBeNull();
  });

  it('shows build errors only for the build source and offers Browse when onBrowse is given', async () => {
    const onBrowse = vi.fn((_mode: string, set: (p: string) => void) => set('/srv/app'));
    const { handle } = setup({
      initial: { source: 'build' },
      errors: { 'build.context': 'build context is required' },
      onBrowse,
    });
    expect(screen.getByLabelText('Build context').getAttribute('aria-invalid')).toBe('true');
    await userEvent.setup().click(screen.getByRole('button', { name: 'Browse for build context' }));
    expect(onBrowse).toHaveBeenCalledWith('dir', expect.any(Function));
    await waitFor(() => expect(handle.current.getForm().buildContext).toBe('/srv/app'));
    await userEvent.setup().click(screen.getByRole('button', { name: 'Browse for Dockerfile' }));
    expect(onBrowse).toHaveBeenLastCalledWith('file', expect.any(Function));
  });

  it('omits Browse buttons without onBrowse', () => {
    setup({ initial: { source: 'build' } });
    expect(screen.queryByRole('button', { name: /Browse/ })).toBeNull();
  });

  it('splits pasted KEY=VALUE lines into rows', async () => {
    const user = userEvent.setup();
    const { handle } = setup({ initial: { env: [{ name: '', value: '' }] } });
    await user.click(screen.getByLabelText('Variable 1 name'));
    await user.paste('A=1\n# comment\nB=x=y\n\nC=');
    await waitFor(() =>
      expect(handle.current.getForm().env).toEqual([
        { name: 'A', value: '1' },
        { name: 'B', value: 'x=y' },
        { name: 'C', value: '' },
      ]),
    );
    expect((screen.getByLabelText('Variable 2 name') as HTMLInputElement).value).toBe('B');
  });

  it('pastes a plain word into the name field unchanged', async () => {
    const user = userEvent.setup();
    const { handle } = setup({ initial: { env: [{ name: '', value: '' }] } });
    await user.click(screen.getByLabelText('Variable 1 name'));
    await user.paste('HOME');
    expect(handle.current.getForm().env).toEqual([{ name: 'HOME', value: '' }]);
  });

  it('updates the bound form on input and add/remove', async () => {
    const user = userEvent.setup();
    const { handle } = setup({ initial: { image: 'a' }, showName: true });
    await user.clear(screen.getByLabelText('Image'));
    await user.type(screen.getByLabelText('Image'), 'redis:7');
    await user.type(screen.getByLabelText('Service name'), '2');
    await user.click(screen.getByRole('button', { name: 'Add port' }));
    await user.type(screen.getByLabelText('Port 1'), '6379:6379');
    await user.type(screen.getByLabelText('CPUs'), '0.5');
    await user.selectOptions(screen.getByLabelText('Restart policy'), 'always');
    expect(handle.current.getForm()).toMatchObject({ image: 'redis:7', ports: ['6379:6379'], cpus: '0.5', restart: 'always' });
    expect(handle.current.getName()).toBe('web2');
    await user.click(screen.getByRole('button', { name: 'Remove item 1' }));
    expect(handle.current.getForm().ports).toEqual([]);
  });

  it('keeps a custom restart value such as on-failure:3 selectable', () => {
    setup({ initial: { restart: 'on-failure:3' } });
    expect((screen.getByLabelText('Restart policy') as HTMLSelectElement).value).toBe('on-failure:3');
  });

  it('handles network mode presets and custom values', async () => {
    const user = userEvent.setup();
    const { handle } = setup({ initial: { networkMode: 'service:db' } });
    expect((screen.getByLabelText('Network mode') as HTMLSelectElement).value).toBe('custom');
    expect((screen.getByLabelText('Custom network mode') as HTMLInputElement).value).toBe('service:db');
    await user.selectOptions(screen.getByLabelText('Network mode'), 'host');
    expect(handle.current.getForm().networkMode).toBe('host');
    expect(screen.queryByLabelText('Custom network mode')).toBeNull();
  });

  it('renders healthcheck fields only when enabled', async () => {
    const user = userEvent.setup();
    const { handle } = setup();
    expect(screen.queryByLabelText('Healthcheck command')).toBeNull();
    await user.click(screen.getByRole('checkbox', { name: 'Enable healthcheck' }));
    await user.type(screen.getByLabelText('Healthcheck command'), 'curl -f localhost');
    expect(handle.current.getForm().healthcheck).toMatchObject({ enabled: true, test: 'curl -f localhost' });
  });

  it('passes the networkPicker render prop the form and its mapped row errors', () => {
    setup({
      withPicker: true,
      initial: { networks: [{ name: 'a', ipv4Address: '' }, { name: 'b', ipv4Address: 'x' }] },
      errors: { 'networks[1].ipv4Address': 'bad ip' },
    });
    expect(screen.getByTestId('picker').textContent).toContain('2 networks');
    expect(screen.getByTestId('picker').textContent).toContain('bad ip');
    expect(screen.getByTestId('picker').textContent).toContain('"1"');
  });

  it('edits networks in a plain list without a picker', async () => {
    const user = userEvent.setup();
    const { handle } = setup();
    await user.click(screen.getByRole('button', { name: 'Add network' }));
    await user.type(screen.getByLabelText('Network 1 name'), 'be');
    await user.type(screen.getByLabelText('Network 1 IPv4 address'), '10.0.0.5');
    expect(handle.current.getForm().networks).toEqual([{ name: 'be', ipv4Address: '10.0.0.5' }]);
  });

  it('shows an editable image tag and its errors in build mode', async () => {
    const { handle } = setup({ initial: { source: 'build', image: 'app:1' }, errors: { image: 'bad tag' } });
    const tag = screen.getByLabelText('Image tag') as HTMLInputElement;
    expect(tag.value).toBe('app:1');
    expect(tag.getAttribute('aria-invalid')).toBe('true');
    expect(screen.getByText('bad tag')).toBeTruthy();
    await userEvent.setup().type(tag, '2');
    expect(handle.current.getForm().image).toBe('app:12');
  });

  it('does not split pasted lines into a read-only environment', async () => {
    const user = userEvent.setup();
    const { handle } = setup({ initial: { env: [{ name: '', value: '' }], readOnly: new Set<Field>(['environment']) } });
    await user.click(screen.getByLabelText('Variable 1 name'));
    await user.paste('A=1\nB=2');
    expect(handle.current.getForm().env).toEqual([{ name: '', value: '' }]);
  });

  it('gives each raw-editor button a group-specific name and shows the reason once', () => {
    setup({ initial: { ports: ['1:1'], volumes: ['a:b'], readOnly: new Set<Field>(['ports', 'volumes']) }, onRawEditor: () => {} });
    expect(screen.getByRole('button', { name: 'Open raw YAML editor for ports' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Open raw YAML editor for volumes' })).toBeTruthy();
    expect(screen.getAllByText(HINT)).toHaveLength(2);
  });

  it('does not count errors on fields that are not shown', () => {
    setup({ errors: { 'healthcheck.interval': 'bad' } });
    expect(screen.getByRole('status').textContent).toBe('');
  });

  it('re-announces when the error set changes even if the count does not', () => {
    const { rerender } = setup({ errors: { image: 'a' } });
    const before = screen.getByRole('status').textContent;
    rerender({ errors: { 'limits.cpus': 'b' } });
    expect(screen.getByRole('status').textContent).not.toBe(before);
  });

  it('announces errors by field label, not by raw path', () => {
    setup({
      initial: { ports: ['x', 'y'], env: [{ name: '', value: '' }] },
      errors: { image: 'a', 'ports[1]': 'b', 'env[0].name': 'c', 'limits.cpus': 'd' },
    });
    const text = screen.getByRole('status').textContent ?? '';
    expect(text).toBe('4 fields have errors: Image, Port 2, Variable 1 name, CPUs');
  });

  it('focuses the first invalid control, or the form alert, via focusFirstError', () => {
    const { handle, rerender } = setup({
      initial: { image: '', ports: ['x'] },
      errors: { 'ports[0]': 'bad port', image: 'image is required' },
    });
    handle.current.focusFirstError();
    expect(document.activeElement).toBe(screen.getByLabelText('Image'));
    rerender({ errors: { _form: 'boom' } });
    handle.current.focusFirstError();
    expect(document.activeElement).toBe(screen.getByRole('alert'));
  });

  it('idPrefix changes every control id', () => {
    setup({ idPrefix: 's2', initial: { ports: ['1:1'] } });
    expect(screen.getByLabelText('Image').id).toBe('s2-image');
    expect(screen.getByLabelText('Port 1').id).toBe('s2-ports-0');
  });

  it('keeps parent changes made between clicking Browse and the picked path arriving', async () => {
    let pick: (path: string) => void = () => {};
    const { handle } = setup({ initial: { source: 'build' }, onBrowse: (_mode, set) => (pick = set) });
    await userEvent.setup().click(screen.getByRole('button', { name: 'Browse for build context' }));
    act(() => handle.current.setForm({ ...handle.current.getForm(), image: 'reset:1' }));
    act(() => pick('/srv'));
    expect(handle.current.getForm()).toMatchObject({ image: 'reset:1', buildContext: '/srv' });
  });

  it('reflects a parent-driven form replacement in the inputs', () => {
    const { handle } = setup({ initial: { image: 'a', ports: ['1:1'] } });
    act(() => handle.current.setForm({ ...emptyForm(), image: 'b', ports: ['2:2', '3:3'] }));
    expect((screen.getByLabelText('Image') as HTMLInputElement).value).toBe('b');
    expect((screen.getByLabelText('Port 2') as HTMLInputElement).value).toBe('3:3');
  });

  it('does not announce build errors while the image source is selected', () => {
    setup({ errors: { 'build.context': 'x' } });
    expect(screen.getByRole('status').textContent).toBe('');
  });
});
