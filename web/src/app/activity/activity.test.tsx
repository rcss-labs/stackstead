import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, expect, it, vi } from "vitest";
import type { DockerEvent } from "$lib/api/types";
import { writable } from "$lib/store";
import {
  installFakeFetch,
  type FakeFetch,
  type Routes,
} from "../../vitest-setup";
import { localScope, ServerContext } from "../servers/ServerContext";
import { ActivityScreen } from "./ActivityScreen";

let fake: FakeFetch | undefined;
afterEach(() => {
  cleanup();
  fake?.restore();
  localStorage.clear();
  vi.restoreAllMocks();
});

const event: DockerEvent = {
  action: "die",
  container: "tika",
  project: "docs",
  service: "tika",
  image: "tika:3",
  time: new Date(Date.now() - 60_000).toISOString(),
  exitCode: 137,
};

function mount(routes: Routes, events: DockerEvent[] = [event]) {
  fake = installFakeFetch({
    "GET /api/operations": {
      body: [
        {
          id: "o1",
          stack: "docs",
          action: "up",
          state: "failed",
          startedAt: new Date(Date.now() - 120_000).toISOString(),
        },
      ],
    },
    "GET /api/actions": { body: [] },
    "GET /api/events/recent": { body: [] },
    "GET /api/backups/runs": {
      body: [
        {
          id: 1,
          startedAt: new Date(Date.now() - 180_000).toISOString(),
          trigger: "schedule",
          status: "success",
          size: 1,
          destinations: [],
          items: [],
        },
      ],
    },
    ...routes,
  });
  const scope = {
    ...localScope,
    events: writable(events),
    href: (p: string) => `/s/mini${p}`,
  };
  return render(
    <MemoryRouter>
      <ServerContext.Provider value={scope}>
        <ActivityScreen />
      </ServerContext.Provider>
    </MemoryRouter>,
  );
}

it("merges every source and links each row through href()", async () => {
  mount({});
  const list = await screen.findByRole("link", {
    name: /Scheduled backup success/,
  });
  expect(list.getAttribute("href")).toBe("/s/mini/backups");
  expect(
    screen.getByRole("link", { name: /tika die/ }).getAttribute("href"),
  ).toBe("/s/mini/containers/tika");
  expect(
    screen.getByRole("link", { name: /up docs/ }).getAttribute("href"),
  ).toBe("/s/mini/stacks/docs");
  const hrefs = screen.getAllByRole("link").map((a) => a.textContent);
  // Newest first: the event (1m) before the operation (2m) before the backup (3m).
  expect(hrefs.findIndex((t) => /tika die/.test(t ?? ""))).toBeLessThan(
    hrefs.findIndex((t) => /up docs/.test(t ?? "")),
  );
  expect(hrefs.findIndex((t) => /up docs/.test(t ?? ""))).toBeLessThan(
    hrefs.findIndex((t) => /Scheduled backup/.test(t ?? "")),
  );
});

it("one failed source shows a notice and keeps the others", async () => {
  mount({ "GET /api/backups/runs": { status: 500, body: { error: "boom" } } });
  expect(await screen.findByText(/Could not load backup runs/)).toBeTruthy();
  expect(screen.getByRole("link", { name: /tika die/ })).toBeTruthy();
  expect(screen.getByRole("link", { name: /up docs/ })).toBeTruthy();
});

it("filter chips narrow by kind", async () => {
  const user = userEvent.setup();
  mount({});
  await screen.findByRole("link", { name: /Scheduled backup/ });
  await user.click(screen.getByRole("button", { name: /^Backups/ }));
  expect(screen.queryByRole("link", { name: /tika die/ })).toBeNull();
  expect(screen.getByRole("link", { name: /Scheduled backup/ })).toBeTruthy();
  expect(
    screen
      .getByRole("button", { name: /^Backups/ })
      .getAttribute("aria-pressed"),
  ).toBe("true");
});

it("summary is omitted on first visit and shown on the next", async () => {
  mount({});
  await screen.findByRole("link", { name: /Scheduled backup/ });
  expect(screen.queryByText(/since your last visit/)).toBeNull();
  expect(
    localStorage.getItem("stackstead.activity.lastVisit.local"),
  ).not.toBeNull();
  cleanup();
  localStorage.setItem(
    "stackstead.activity.lastVisit.local",
    String(Date.now() - 3_600_000),
  );
  mount({});
  expect(
    await screen.findByText(
      /1 operation, 1 Docker event and 1 backup run since your last visit/,
    ),
  ).toBeTruthy();
});

it("screen states how far each source reaches", async () => {
  mount({});
  const region = await screen.findByRole("region", {
    name: "How far back this goes",
  });
  expect(within(region).getByText(/last 50 the server saw/)).toBeTruthy();
  expect(within(region).getByText(/kept for one hour/)).toBeTruthy();
  expect(within(region).getByText(/newest 100/)).toBeTruthy();
  expect(within(region).getByText(/Backup runs: the newest 50/)).toBeTruthy();
  await waitFor(() =>
    expect(within(region).getAllByText(/Oldest shown/).length).toBeGreaterThan(
      0,
    ),
  );
});

const VISIT = "stackstead.activity.lastVisit.local";

it("a failed source never claims nothing is new, and does not consume the last visit", async () => {
  const before = String(Date.now() - 3_600_000);
  localStorage.setItem(VISIT, before);
  mount(
    {
      "GET /api/backups/runs": { status: 500, body: { error: "boom" } },
      "GET /api/operations": { status: 500, body: { error: "boom" } },
    },
    [],
  );
  expect(await screen.findByText(/Could not load backup runs/)).toBeTruthy();
  await waitFor(() => expect(screen.queryByText(/Nothing new/)).toBeNull());
  expect(screen.getByText(/could not check/i)).toBeTruthy();
  expect(localStorage.getItem(VISIT)).toBe(before);
  expect(
    screen.queryByText(
      /No events, operations, actions or backup runs are recorded/,
    ),
  ).toBeNull();
});

it("records the visit once every source loaded", async () => {
  mount({});
  await screen.findByRole("link", { name: /Scheduled backup/ });
  await waitFor(() => expect(localStorage.getItem(VISIT)).not.toBeNull());
});

it("shows recent Docker events fetched from the server after a reload, once each", async () => {
  const recent = {
    ...event,
    container: "old-one",
    time: new Date(Date.now() - 600_000).toISOString(),
  };
  mount({ "GET /api/events/recent": { body: [recent, event] } });
  expect(await screen.findByRole("link", { name: /old-one die/ })).toBeTruthy();
  expect(screen.getAllByRole("link", { name: /tika die/ })).toHaveLength(1);
});

it("a failed recent-events fetch shows a notice and keeps live events", async () => {
  mount({ "GET /api/events/recent": { status: 500, body: { error: "nope" } } });
  expect(await screen.findByText(/Could not load Docker events/)).toBeTruthy();
  expect(screen.getByRole("link", { name: /tika die/ })).toBeTruthy();
});

it("image operations link to the image page", async () => {
  mount({
    "GET /api/operations": {
      body: [
        {
          id: "o2",
          stack: "image/acme/web-app",
          action: "pull",
          state: "succeeded",
          startedAt: new Date(Date.now() - 120_000).toISOString(),
        },
      ],
    },
  });
  expect(
    (
      await screen.findByRole("link", { name: /pull image web-app/ })
    ).getAttribute("href"),
  ).toBe("/s/mini/images/web-app");
});
