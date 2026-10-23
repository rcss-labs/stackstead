import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it } from 'vitest';
import { installFakeFetch, type FakeFetch, type Routes } from '../../vitest-setup';
import { ImageDefaultsCard } from './ImageDefaultsCard';

let fake: FakeFetch;
afterEach(() => fake.restore());

function mount(retention: { keepLast: number; keepDays: number }, routes: Routes = {}) {
  fake = installFakeFetch({ 'GET /api/settings/images': { body: { retention } }, ...routes });
  return render(<ImageDefaultsCard />);
}
const puts = () => fake.calls.filter((c) => c.method === 'PUT' && c.path === '/api/settings/images');

describe('ImageDefaultsCard', () => {
  it('shows the current default as retention text and the safety note', async () => {
    mount({ keepLast: 5, keepDays: 30 });
    expect(await screen.findByText('Keep the last 5 and anything from the last 30 days')).toBeTruthy();
    expect(screen.getByText(/never removes the newest successful version or a version in use/i)).toBeTruthy();
    expect(screen.getByText(/only cleans up on this server/i)).toBeTruthy();
  });

  it('says Keep forever when no default is set', async () => {
    mount({ keepLast: 0, keepDays: 0 });
    expect(await screen.findByText('Keep forever')).toBeTruthy();
  });

  it('saves {retention:{keepLast, keepDays}} and shows the answer', async () => {
    mount({ keepLast: 0, keepDays: 0 }, { 'PUT /api/settings/images': { body: { retention: { keepLast: 3, keepDays: 0 } } } });
    const last = await screen.findByLabelText('Keep last');
    await userEvent.clear(last);
    await userEvent.type(last, '3');
    await userEvent.click(screen.getByRole('button', { name: 'Save default' }));
    await waitFor(() => expect(puts()).toHaveLength(1));
    expect(puts()[0].body).toEqual({ retention: { keepLast: 3, keepDays: 0 } });
    expect(await screen.findByText('Keep the last 3')).toBeTruthy();
  });

  it('blocks saving a value above the limit without calling the server', async () => {
    mount({ keepLast: 0, keepDays: 0 });
    const last = await screen.findByLabelText('Keep last');
    await userEvent.clear(last);
    await userEvent.type(last, '5000');
    expect(await screen.findByText(/whole number from 0 to 1000/)).toBeTruthy();
    await userEvent.click(screen.getByRole('button', { name: 'Save default' }));
    expect(puts()).toHaveLength(0);
  });

  it('shows the server error when the save is refused', async () => {
    mount({ keepLast: 0, keepDays: 0 }, { 'PUT /api/settings/images': { status: 400, body: { error: 'retention values are not accepted' } } });
    const last = await screen.findByLabelText('Keep last');
    await userEvent.clear(last);
    await userEvent.type(last, '5');
    await userEvent.click(screen.getByRole('button', { name: 'Save default' }));
    expect(await screen.findByText(/retention values are not accepted/)).toBeTruthy();
  });
});
