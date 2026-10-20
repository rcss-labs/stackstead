import { describe, expect, it, vi } from 'vitest';
import { createEditor } from './cm';

describe('createEditor (real CodeMirror in jsdom)', () => {
  it('mounts with the value, reports edits, supports setValue and destroy', () => {
    const parent = document.createElement('div');
    document.body.append(parent);
    const onchange = vi.fn();
    const h = createEditor({ parent, value: 'a: 1\n', readonly: false, ariaLabel: 'Compose file', onchange });
    expect(parent.querySelector('.cm-content')?.getAttribute('aria-label')).toBe('Compose file');
    expect(h.getValue()).toBe('a: 1\n');
    h.setValue('b: 2\n');
    expect(onchange).toHaveBeenCalledWith('b: 2\n');
    h.destroy();
    expect(parent.querySelector('.cm-editor')).toBeNull();
  });

  it('createEditor with language dockerfile tags the content as dockerfile', () => {
    const parent = document.createElement('div');
    document.body.append(parent);
    const h = createEditor({ parent, value: 'FROM alpine\n', readonly: false, ariaLabel: 'Dockerfile', onchange: vi.fn(), language: 'dockerfile' });
    expect(parent.querySelector('.cm-content')?.getAttribute('data-language')).toBe('dockerfile');
    h.destroy();
  });

  it('createEditor defaults to yaml', () => {
    const parent = document.createElement('div');
    document.body.append(parent);
    const h = createEditor({ parent, value: 'a: 1\n', readonly: false, ariaLabel: 'Compose file', onchange: vi.fn() });
    expect(parent.querySelector('.cm-content')?.getAttribute('data-language')).toBe('yaml');
    h.destroy();
  });

  it('setAriaLabel renames the editor for assistive tech', () => {
    const parent = document.createElement('div');
    document.body.append(parent);
    const h = createEditor({ parent, value: '', readonly: false, ariaLabel: 'Dockerfile for web', onchange: vi.fn() });
    h.setAriaLabel('Dockerfile for api');
    expect(parent.querySelector('.cm-content')?.getAttribute('aria-label')).toBe('Dockerfile for api');
    h.destroy();
  });

  it('setAriaState ties an error to the editor and clears it again', () => {
    const parent = document.createElement('div');
    document.body.append(parent);
    const h = createEditor({ parent, value: '', readonly: false, ariaLabel: 'Dockerfile', onchange: vi.fn() });
    const content = () => parent.querySelector('.cm-content');
    h.setAriaState({ invalid: true, describedBy: 'err-1' });
    expect(content()?.getAttribute('aria-invalid')).toBe('true');
    expect(content()?.getAttribute('aria-describedby')).toBe('err-1');
    expect(content()?.getAttribute('aria-label')).toBe('Dockerfile');
    h.setAriaState({});
    expect(content()?.hasAttribute('aria-invalid')).toBe(false);
    expect(content()?.hasAttribute('aria-describedby')).toBe(false);
    h.destroy();
  });
});
