# Stackstead Plan 18c — React stack detail (c): the resizable live log console

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **Plan format:** tasks, names, signatures, test cases and examples only; no code bodies. Implementers write everything test-first.

**Goal:** Fill the console slot of the stack detail's right pane with the Evolve v2 log console: a `--chassis` drawer pinned to the bottom with a drag handle (120–560px, persisted under `stackstead.logsHeight`, fixed 280px on mobile, .22 s height transition when not dragging), a header with the collapse chevron, the Follow / Paused pill (`Paused · N new`, resume jumps to the bottom), the All / Warn+ / Errors level segmented control and a search input, service filter chips in series colours, the line grid (`64px 110px 1fr`, `58px 1fr` on mobile) with ERROR / WARN tones, a 1.2 s flash for new lines, click-to-pin, auto-scroll while following, and the latest line inline when collapsed — on top of the unchanged `createLogStream` + `createLogBuffer` resume semantics, with every `LogsViewer.test.ts` behaviour that survives the design (divergence 6 lists the two that do not) ported as `.test.tsx` cases. The same component takes a container source so Plan 19 can reuse it.

**Architecture:** `web/src/app/detail/LogConsole.tsx` is the only new consumer of `lib/stacks/logs.ts`; it owns the console state the README lists (`logsOpen`, `logsHeight`, `follow`, `pausedAt` as `frozenSeq`, `levelFilter`, `serviceFilter`, `logSearch`, `pinnedLine`) and nothing else. `useLogStream(source, options)` holds one `createLogStream` per mount (lazy `useState`), subscribes to its five stores through `useStore`, and exposes `restart` / `clear`. `useLogsHeight()` reads the persisted height once, writes on drag end, and clamps. Lines are derived with the 18a `console.ts` helpers. The screen mounts the console once the detail is loaded (divergence 8) and passes the location's `logsRequested` / `logService` to expand it and preselect its chip; the service pane's `Logs` button and the service header's chip selection go through `onFilterService`. Flash and pulse are CSS keyframes only.

**Tech Stack:** React 19.3.1, CSS Modules, Vitest + jsdom + @testing-library/react + user-event, fake timers, `installFakeStream`. No new dependencies.

**Spec:** spec §4 (an SSE resume that drops or duplicates log lines is a failure), §8 "Log console" (reuses `stacks/logs.ts` + `logs/buffer.ts`, `ERROR` / `WARN` tokens, height key and clamp), §6.3, §6.4. README log console paragraph, "Motion" (hover .12–.15 s, flash, all disabled under reduced motion), "State". **Depends on 18a** (`console.ts`: `LogLevel`, `filterConsoleLines`, `newLinesSince`, `searchSegments`, `clampLogsHeight`, `readLogsHeight`, `writeLogsHeight`, `logClock`, `logUtc`, `consoleStatus`) **and 18b** (the console slot, `useDetailLocation`'s `logsRequested` / `logService`, the `Logs` button of the service pane).

## Spec divergences

18a's divergences 3–8 apply (level filter from `lineTone`, pill = pause, exclusive chips restarting the stream, Stream / Tail selects dropped, height default 240, stream for the page lifetime). Additions (author's calls):

1. **Collapsed header content**: when collapsed the header shows the latest visible line (`<clock> <service> <text>` mono, ellipsis) after the pill; the search, level control and chips are hidden; the stream keeps running (divergence 8), so the line is live.
2. **Chip colours** use `var(--series-<n>)` with `n = serviceColorIndex(service, services) + 1` (the design's four series colours are `--series-1..4`; stacks with more services wrap after 8); the line's service column uses the same colour instead of today's hashed hue.
3. **`?tab=logs` is honoured once**: on mount (or when the query changes to it) the console expands and selects the chip; later collapsing does not rewrite the URL. `Logs` on the service header expands the console and selects that service's chip without navigating (the URL keeps the pane's item); the overview's `Logs` link (`?tab=logs`) still works through the resolver.
4. **Pinned line**: clicking a line toggles `pinnedLine` (its `seq`); the pinned row gets `data-pinned` and `aria-pressed` (rows are `button`-like `div`s with `role="button"` and `tabIndex=0`, Enter / Space toggles); the pin survives filtering and clears on `Clear`, `restart` and unmount. Auto-scroll keeps following while a line is pinned (the pin is a highlight, not a scroll lock).
5. **Dropped / error / ended notices** keep today's text and roles (`<n> older lines dropped`, per-container `role="alert"` with `Dismiss error for <container>`, `end of log`, `Reconnect`) rendered as thin rows above the lines; `Clear` is an icon button labelled `Clear log`.
6. **Search debounce** stays 150 ms; the `mark` highlight stays.

## Global Constraints

- SSE semantics untouched: the console never touches `Last-Event-ID` logic, cursors or the coalescer; it calls `stream.restart(options)` only on a chip change or `Reconnect`, `stream.clear()` on Clear, `stream.close()` on unmount; `tail: 200`, `follow: true` always; `service` from the chip.
- Console state is local (README "State"); nothing is persisted but `logsHeight`; `localStorage` access goes through the 18a helpers (try/catch inside).
- Rendering stays bounded: the visible list is the filtered snapshot of the buffer (≤ 5000 rows), keyed by `seq`; a flood test asserts the bound and the dropped note.
- Motion: height transition `.22s` only when not dragging (`data-dragging` disables it), chevron rotate .2 s, flash keyframes `lineIn` 1.2 s applied to newly mounted rows (CSS animation on mount; no JS timers), pill dot pulse 2 s while following; all neutralised by the 16a reduced-motion rule.
- Accessibility: the drawer is a `section` labelled `Logs`; the collapse control is a `button` with `aria-expanded`; the pill is a `button` with `aria-pressed` (pressed = paused) named `Following` / `Paused · N new`; the level control is `Segmented label="Level"`; chips are `button`s with `aria-pressed`; the search input is `aria-label="Search logs"`; the line region is `role="log" aria-live="off" aria-label="Log output" tabindex=0`; the status text is `role="status"`; the drag handle is a `div role="separator" aria-orientation="horizontal" aria-label="Resize logs" tabindex=0` with `aria-valuenow` and Arrow Up / Down changing the height by 20px (keyboard parity for the drag); every target ≥ 40px under 760px (header controls 40px tall on mobile).
- Eagerness: no new entry imports; the console is part of the `StackDetail` chunk; entry line recorded per task, unchanged.
- Test-first, conventional commits, no code bodies in this plan.

## Review Focus

1. Resume without drop or duplication: a reconnect after a disconnect sends the composite `Last-Event-ID` and renders each line once; a chip change restarts with no `Last-Event-ID` and shows only the new stream's lines; `Reconnect` after `end` opens a second request. (Task 2.)
2. Pause semantics: paused freezes the visible rows and counts new lines in the pill; resume shows them all and scrolls to the bottom; a chip change while paused un-pauses; a session-expired or reconnecting stream still updates the status while paused. (Task 2.)
3. Height persistence: the initial height comes from `stackstead.logsHeight` (clamped), a drag writes the clamped final value once, a throwing storage never breaks the console, and under 760px the height is 280 with no handle. (Task 1.)
4. Stream lifetime: the stream opens once the detail is loaded (not before, not on 404 / error), stays open while collapsed, is restarted (not re-created) by chips, and is aborted on unmount and on `a → b` navigation. (Task 3.)
5. Levels and tones: `Warn+` keeps warning and danger lines, `Errors` keeps danger only, the row tone matches `lineTone`, search and level combine, and `No lines match.` appears only when filters hide every line of a non-empty buffer. (Task 2.)

---

## File structure

| Area | Files | Tasks |
|---|---|---|
| Hooks, drawer, header, resize | `web/src/app/detail/useLogStream.ts`, `web/src/app/detail/useLogsHeight.ts`, `web/src/app/detail/LogConsole.tsx` (+ `.module.css`), `web/src/app/detail/LogConsoleHeader.tsx`, `web/src/app/detail/logConsole.test.tsx` | T1 |
| Lines, chips, filters, notices | `web/src/app/detail/LogLines.tsx` (+ `.module.css`), `web/src/app/detail/LogChips.tsx`, `web/src/app/detail/logLines.test.tsx` | T2 |
| Page integration, mobile, docs | `web/src/app/detail/StackDetailScreen.tsx`, `web/src/app/detail/ServicePane.tsx`, `web/src/app/detail/stackDetail.test.tsx`, `docs/development.md` | T3 |

Order: T1 → T2 → T3. Sonnet implements test-first, Opus reviews each task, Fable reviews the branch.

---

### Task 1: Stream and height hooks, drawer shell, header, drag handle

README console header, drag handle, collapsed state; 18a divergences 4, 7, 8; this plan's 1, 3. Review focus 3.

**Files:**
- Create: `web/src/app/detail/useLogStream.ts`, `web/src/app/detail/useLogsHeight.ts`, `web/src/app/detail/LogConsole.tsx` + `.module.css`, `web/src/app/detail/LogConsoleHeader.tsx`, `web/src/app/detail/logConsole.test.tsx`

**Interfaces:**
- `useLogStream(source: LogSource, initial: LogStreamOptions): { lines: LogLine[]; status: SSEStatus; errors: LogError[]; ended: boolean; dropped: number; restart: (opts: LogStreamOptions) => void; clear: () => void }` — lazy `useState(() => createLogStream(source, initial))`; `close()` in the unmount cleanup; stores read with `useStore` (the buffer is a `Readable<LogLine[]>`).
- `useLogsHeight(storage: Storage | undefined = window.localStorage): { height: number; setHeight: (h: number) => void; commit: () => void }` — `height` initialised from `readLogsHeight(storage)`; `setHeight` clamps; `commit` writes through `writeLogsHeight` (called on drag end and after a keyboard step).
- `LogConsole { source: LogSource; services: string[]; service: string | undefined; onService: (service: string | undefined) => void; open: boolean; onOpenChange: (open: boolean) => void; mobile: boolean }` — `section aria-label="Logs"` on `--chassis` with `border-top`; renders `LogConsoleHeader`, the drag handle (only when `open && !mobile`), and the body (`height: open ? (mobile ? 280 : height) : 0`, `overflow: hidden`, `data-dragging`, transition `.22s` unless dragging) containing `LogChips` + `LogLines` (T2; T1 renders the body empty). Owns `paused` / `frozenSeq` / `level` / `search` / `pinned` (T2 consumes them) and the stream through `useLogStream(source, { service, tail: 200, follow: true })`; a `service` prop change calls `restart` with the new filter (and un-pauses, clears the pin).
- `LogConsoleHeader { open: boolean; onToggle: () => void; paused: boolean; newCount: number; onTogglePause: () => void; level: LogLevel; onLevel: (l: LogLevel) => void; search: string; onSearch: (s: string) => void; status: { label: string; tone: 'live' | 'warning' | 'muted' }; latest: LogLine | undefined }` — chevron `button` (`aria-expanded`, text `Logs`, chevron rotates 180° when open), pill `button` (`aria-pressed={paused}`, label `Following` with a pulsing 6px dot or `Paused` / `Paused · <n> new`, success-soft / warning-soft), the collapsed latest line (divergence 1) or, when open, spacer + `Segmented label="Level" size="sm"` (All / Warn+ / Errors) + `input type="search" aria-label="Search logs" placeholder="Search logs"` (mono, `--bg`, 26px; 40px on mobile) and the `role="status"` text (`consoleStatus`).
- Drag handle: `div role="separator" aria-orientation="horizontal" aria-label="Resize logs" aria-valuemin=120 aria-valuemax=560 aria-valuenow={height} tabindex=0 title="Drag to resize"` (8px tall, 36×3 grip, `cursor: row-resize`); `pointerdown` captures the pointer, `pointermove` sets `height = clamp(start − dy)`, `pointerup` / `pointercancel` commits; Arrow Up / Down ±20 then commit.

- [ ] **Step 1: Write the failing tests.** `logConsole.test.tsx` (direct renders with `installFakeStream` on `/api/stacks/web/logs`, fake timers, a `storage` stub passed through a `storage` prop or a module-level override; the `logEvent()` helper from `LogsViewer.test.ts`): `opens one stream with follow and tail 200 and aborts it on unmount` (request path contains `tail=200` and `follow=1`, no `service=`; unmount → `signal.aborted`); `service prop preselects the server filter` (`service=worker` in the first request); `an unknown service falls back to all services` (no `service=` and `onService(undefined)` not required — the chip selection in T2 handles it); `collapsed shows the latest line and keeps the stream` (focus 4: `open: false` → header text contains the last line's text, no body rows, request not aborted); `the chevron toggles through onOpenChange with aria-expanded`; `the pill reads Following with a live dot and Paused after a click` (`aria-pressed`); `initial height comes from storage, clamped` (focus 3: `'300'` → body `style.height` `300px`; `'999'` → `560px`; missing → `240px`); `dragging the handle resizes and writes the clamped value once on release` (focus 3: pointer events on the separator from `clientY` 400 to 300 → height +100, `aria-valuenow`, one `setItem('stackstead.logsHeight', …)` after `pointerup`, none during the move; `data-dragging` present during the move and gone after); `keyboard arrows step the height by 20 and commit`; `a throwing storage leaves the console usable` (focus 3; default 240, no error thrown on release); `mobile is fixed at 280 with no handle` (focus 3: `mobile: true` → no `separator`, height `280px`); `the status text reflects the stream` (`live`, then `reconnecting` in warning after `fake.close`, `session expired - sign in again` on 401 while paused).
- [ ] **Step 2: Run** `cd web && npx vitest run src/app/detail/logConsole` — FAIL: unresolved imports.
- [ ] **Step 3: Implement** (header `min-height 38px; padding 0 12px; gap 8px; flex-wrap`; chevron 30px; pill 24px radius 999; level control 24px in a `--border-strong` 6px box; search 26px; `@media (max-width: 759px)`: controls 40px tall, search full width on a second row).
- [ ] **Step 4: Run** `make ui-test`, `make ui-check`, `npm run build:app` (entry line; unchanged).
- [ ] **Step 5: Commit** `feat(web): log console drawer with follow pill, level control, search and persisted resize` (why: the drawer's lifetime and height rules are pinned before any line renders, so the stream contract cannot be bent by rendering concerns later).

---

### Task 2: Lines, chips, filters, notices, pause and pin

README line grid, chips, flash, pin, auto-scroll; spec §4 resume; 18a divergences 3–6; this plan's 2, 4, 5, 6. Ports the `LogsViewer` cases. Review focus 1, 2 and 5.

**Files:**
- Create: `web/src/app/detail/LogLines.tsx` + `.module.css`, `web/src/app/detail/LogChips.tsx`, `web/src/app/detail/logLines.test.tsx`
- Modify: `web/src/app/detail/LogConsole.tsx` (mounts chips, lines and notices; pause / pin / level / search state)

**Interfaces:**
- `LogChips { services: string[]; selected: string | undefined; onSelect: (service: string | undefined) => void }` — `div` `aria-label="Service filter"`: `All` chip then one per service (24px, radius 999, mono 11px, 6px square in `var(--series-n)`), `aria-pressed`; hidden when `services.length === 0` (container source).
- `LogLines { lines: LogLine[]; search: string; pinned: number | undefined; onPin: (seq: number) => void; paused: boolean; ended: boolean; dropped: number; errors: LogError[]; onDismissError: (e: LogError) => void; onReconnect: () => void; onClear: () => void; colorIndex: (service: string) => number; empty: boolean }` — notices row(s): `<n> older lines dropped` (muted), per-error `div role="alert"` (`<service> (<container>)`, message as text, `Dismiss error for <container>` button), `Clear log` icon button; the scroller `div role="log" aria-live="off" aria-label="Log output" tabindex=0` (mono 11.5px / 1.75, padding 0 12px 10px) with rows `div role="button" tabindex=0 data-testid="log-row" data-tone={lineTone} data-pinned aria-pressed` (grid `64px 110px minmax(0,1fr)`, gap 10px; mobile `58px minmax(0,1fr)` with the service hidden): `time` (`logClock`, `title` = `logUtc`, `dateTime`), service `span` (colour from `colorIndex`, ellipsis) with the stderr tag (`Tag` `stderr`, mono) when `stream === 'stderr'`, text `span` built from `searchSegments` (`mark` for matches); `No lines match.` (faint) when `empty`; `end of log` + `Reconnect` button when `ended`; auto-scroll: a `stick` ref updated on scroll (`< 24px` from the bottom), `scrollTop = scrollHeight` after each lines change while `stick && !paused`; a `Jump to latest` button when paused with `newCount > 0` (today's `newbar`), also offered by the pill.
- `LogConsole` state: `paused` / `frozen` / `frozenSeq` (toggle = today's `togglePause`; a chip change or `Reconnect` resets them), `view = paused ? frozen : lines`, `visible = filterConsoleLines(view, { level, search })`, `newCount = paused ? newLinesSince(lines, frozenSeq) : 0`, `dismissed` set keyed `container\0error`, `pinned` seq.

- [ ] **Step 1: Write the failing tests.** `logLines.test.tsx` (through `LogConsole` with `installFakeStream`, fake timers, `rows()` helper on `[data-testid="log-row"]`): `TestFloodRendersBoundedRows` (verbatim: 20 000 events → ≤ 5000 rows and `15,000 older lines dropped`); `renders text as text and filters by search` (verbatim incl. `mark`); `levels filter by lineTone` (focus 5: lines `ok`, `WARN w`, `ERROR e` → `Warn+` keeps two, `Errors` keeps one, `data-tone` warning / danger); `No lines match only when filters hide a non-empty buffer` (focus 5; empty buffer → no text); `service chips restart the stream with the new query and show only its lines` (focus 1: chip `worker` → last request `service=worker`, no `Last-Event-ID`, one row); `the All chip clears the filter`; `a reconnect resumes from the composite cursor without repeating lines` (focus 1: two containers with ids, `fake.close` → the second request's `Last-Event-ID` is `<a-id>,<b-id>`; a line pushed on the new response renders once; rows count = 3); `shows per-container errors, dismissible` (verbatim) and `labels dismiss buttons and keeps the log region quiet for screen readers` (verbatim: `aria-live="off"`); `words the gone status by source kind` (`it.each`, verbatim); `shows end of log when the stream ends` and `offers Reconnect after the stream ends and Clear resets the new-lines count` (verbatim, `Clear log` name); `pause freezes the view and counts new lines in the pill` (focus 2: click `Following` → push two → one row, pill `Paused · 2 new`; click → three rows, scroller `scrollTop === scrollHeight` with stubbed metrics); `chip change while paused unpauses` (focus 2); `while paused, a session-expired stream still announces it` and `while paused, a reconnecting stream reads reconnecting in the warning tone` (verbatim against `data-tone` on the status); `container source hides the chips and opens the container URL` (verbatim); `clicking a line pins it and clicking again unpins; Clear drops the pin` (`data-pinned`, `aria-pressed`); `stderr lines carry the stderr tag`; `service colours follow the sorted index` (two services → `--series-1` / `--series-2` in the chip style); `auto-scroll sticks to the bottom only when near it` (stub `scrollHeight` / `clientHeight`, set `scrollTop` away → a new line does not move it; back near → it does).
- [ ] **Step 2: Run** `cd web && npx vitest run src/app/detail/logLines` — FAIL: unresolved imports.
- [ ] **Step 3: Implement** (row hover `--hover`, pinned `--accent-soft` background, `lineIn` keyframes `accent-soft → transparent` 1.2 s on row mount; mobile grid `58px minmax(0,1fr)`).
- [ ] **Step 4: Run** `make ui-test`, `make ui-check`, `npm run build:app` (entry line; unchanged).
- [ ] **Step 5: Commit** `feat(web): console lines with service chips, levels, pause, pin and resume-safe reconnects` (why: this is where the spec's anti-goal lives — a resume that drops or duplicates — so the cursor and chip cases are pinned against the real stream module).

---

### Task 3: Page integration, `?tab=logs`, mobile pass, docs

README "backed by existing LogsViewer logic"; 18a divergences 8, 18; this plan's 3. Review focus 4.

**Files:**
- Modify: `web/src/app/detail/StackDetailScreen.tsx` (mounts `LogConsole` in the `SplitPane` console slot once `detail` is loaded; owns `logsOpen` and `logService`), `web/src/app/detail/ServicePane.tsx` (`onLogs` → expand + select), `web/src/app/detail/stackDetail.test.tsx`, `docs/development.md`

**Interfaces:**
- Screen state: `logsOpen` initialised to `!mobile` (from `useIsMobile()`), set to `true` whenever the location's `logsRequested` becomes true (effect keyed on `location.search`); `logService` initialised from `logService` of the location and updated by chips (`onService`) and by the pane's `Logs` button (`onLogs(service)` → `setLogService(service)`, `setLogsOpen(true)`); the console is rendered only while `detail` is defined (and not `notFound`), with `source = { kind: 'stack', name }`, `services = detail.services.map(s => s.name)`.
- `docs/development.md`: one paragraph: the console keeps one log stream per open stack page (tail 200, follow), restarted only by the service chips, height persisted under `stackstead.logsHeight`.

- [ ] **Step 1: Write the failing tests.** In `stackDetail.test.tsx`: `?tab=logs expands the console with the service chip preselected` (`/stacks/web?tab=logs&service=app` → `aria-expanded="true"`, chip `app` pressed, request `service=app`, the right pane shows `app`'s overview); `the console stream opens once the stack is loaded and not before` (focus 4: gated `GET /api/stacks/web` → no `/logs` request until released); `no log stream on 404 or load failure` (focus 4); `collapsing keeps the stream and shows the latest line` (focus 4); `the pane's Logs button expands the console and selects its service without navigating` (location unchanged, chip `db` pressed, request `service=db`); `navigating to another stack aborts the console stream and opens the new stack's` (focus 4: `web`'s log request `signal.aborted`, one `/api/stacks/db/logs` request); `mobile starts collapsed at 280px` (`matchMedia` stub for `(max-width: 759px)` → `aria-expanded="false"`, expanding → height `280px`, no separator); `the unsaved-changes guard does not interfere with console actions` (with `dirty` forced through `setUnsavedChanges(true)`, chip and pill clicks never open the discard dialog).
- [ ] **Step 2: Run** `cd web && npx vitest run src/app/detail/stackDetail` — FAIL: no console in the slot.
- [ ] **Step 3: Implement**; mobile pass with `npm run dev:app` at 390px (record: console collapsed by default, expands to 280px, header controls 40px, chips scroll horizontally, no horizontal page scroll) and 1280px (drag between 120 and 560, persisted across reloads).
- [ ] **Step 4: Run** `npm run build:app` (entry line unchanged; `StackDetail` chunk size noted), `make check`, `make ui`; manual pass: stack with a noisy service → lines flash in, `Warn+` thins them, pause shows the count, resume jumps to the bottom, a chip restarts with only that service, stop Docker → status reads reconnecting in warning while paused.
- [ ] **Step 5: Commit** `feat(web): mount the log console on the stack detail with ?tab=logs and per-service Logs` (why: the console is the page's always-on window into the stack; wiring it last keeps the stream-lifetime rule testable at the page level).

---

## Self-review notes

- Spec coverage: §4 resume anti-goal (T2 cursor cases), §8 log console paragraph (levels T2, height key / clamp T1, reuse of `logs.ts` + `buffer.ts` throughout), §6.3 / §6.4 (every task); README console: drag handle (T1), header pill / level / search (T1), chips (T2), line grid / tones / flash / pin / auto-scroll (T2), collapsed latest line (T1), mobile fixed 280 (T1, T3); README State: the eight console keys are the console's state (T1, T2) with `logsOpen` on the screen (T3).
- No code bodies: hook signatures, props, roles, strings and test names with assertions only.
- Type consistency: `LogSource` / `LogStreamOptions` / `LogLine` / `LogError` / `SSEStatus` are the untouched lib shapes `useLogStream` returns; `LogLevel` and the `consoleStatus` result (18a T2) are the header's props; `serviceColorIndex` (lib/stacks/metrics.ts) is the `colorIndex` source; `DetailLocation.logsRequested` / `logService` (18a T1) drive T3; `SplitPane.console` (18b T3) is the slot.
- Review focus: 1 → `logLines.test.tsx` reconnect and chip cases; 2 → its pause cases; 3 → `logConsole.test.tsx` height cases; 4 → the `stackDetail.test.tsx` lifetime cases in T3 plus the collapsed case in T1; 5 → the level cases.
- Proportion: T2 is the largest because the Svelte viewer tests are the spec of the stream contract; T1 isolates the drawer mechanics; T3 is the integration and mobile pass, last so the lifetime rule is tested on the finished page.
