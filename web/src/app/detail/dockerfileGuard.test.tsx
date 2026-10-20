import { cleanup, fireEvent, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { get } from '$lib/store';
import { unsavedChanges } from '$lib/stores/guard';
import { installFakeFetch, installFakeStream } from '../../vitest-setup';

vi.mock('$lib/stacks/cm', () => ({
  createEditor: () => {
    throw new Error('chunk unavailable in this test');
  },
  createDiffView: () => {
    throw new Error('chunk unavailable in this test');
  },
}));

import { appService, installHarness, mountDetail, removeHarness, stackDetail, type Harness } from './detailTestKit';

let h: Harness;
beforeEach(() => {
  h = installHarness();
  h.operations = [];
  h.stackRes = { body: stackDetail({ services: [appService({ name: 'app', config: { build: { context: '.' }, limits: {} } })] }) };
  h.stream.restore();
  h.fake.restore();
  h.fake = installFakeFetch({
    'GET /api/stacks/web': () => h.stackRes,
    'GET /api/stacks/web/operations': { body: [] },
    'GET /api/stacks/web/health?span=60m': { body: { from: 0, to: 3600, services: [] } },
    'GET /api/stacks/web/metrics': { body: { points: [] } },
    'GET /api/stacks/web/compose': { body: { content: 'services: {}\n', hash: 'c1' } },
    'GET /api/stacks/web/services/app/dockerfile': { body: { content: 'FROM alpine:3.20\n', hash: 'd1', exists: true, editable: true, path: 'Dockerfile' } },
    'GET /api/status': { body: { docker: 'ok', compose: '2.29.0', events: 'connected', metricsDegraded: false } },
    'GET /api/stacks': { body: [] },
    'GET /api/operations?state=running': { body: [] },
  });
  h.stream = installFakeStream({ '/api/events': [], '/api/stacks/web/logs': [], '/api/stacks/web/metrics/live': [] });
});
afterEach(async () => {
  cleanup();
  await removeHarness(h);
});

describe('Dockerfile tab on the stack page', () => {
  it('leaving with an unsaved Dockerfile edit asks first', async () => {
    mountDetail(h, '/stacks/web?service=app&tab=dockerfile');
    const ta = (await screen.findByRole('textbox', { name: 'Dockerfile for app' })) as HTMLTextAreaElement;
    await waitFor(() => expect(ta.value).toBe('FROM alpine:3.20\n'));
    expect(get(unsavedChanges)).toBe(false);
    fireEvent.change(ta, { target: { value: 'FROM alpine:3.21\n' } });
    await waitFor(() => expect(get(unsavedChanges)).toBe(true));
    await userEvent.setup().click(screen.getByRole('tab', { name: 'Overview' }));
    expect(await screen.findByRole('alertdialog', { name: 'Discard unsaved changes?' })).toBeTruthy();
  });
});
