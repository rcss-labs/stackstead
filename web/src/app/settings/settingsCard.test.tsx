import { render } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { SettingsCard } from './SettingsCard';

describe('SettingsCard', () => {
  it.each([false, null, undefined])('renders no footer for %s', (footer) => {
    const { container } = render(<SettingsCard id="c-h" title="Card" footer={footer}>body</SettingsCard>);
    expect(container.querySelector('footer')).toBeNull();
  });

  it.each([false, null, undefined])('renders no header spacer for headerEnd %s', (headerEnd) => {
    const { container } = render(<SettingsCard id="c-h" title="Card" headerEnd={headerEnd}>body</SettingsCard>);
    expect(container.querySelector('header')?.children).toHaveLength(1);
  });

  it('renders the footer and the header end when given', () => {
    const { container } = render(
      <SettingsCard id="c-h" title="Card" headerEnd={<b>end</b>} footer="note">body</SettingsCard>,
    );
    expect(container.querySelector('footer')?.textContent).toBe('note');
    expect(container.querySelector('header')?.textContent).toContain('end');
  });
});
