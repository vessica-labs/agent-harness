---
name: test-control-plane-ui
description: Run and manually test the cloud-runner control plane dashboard (cloud-runner/internal/ui/index.html) in a browser without a real Railway control plane, using a small fake HTTP/SSE server. Use when verifying dashboard UI changes such as panels, the input inbox, live event rendering, or the mascot.
---

# Test the control-plane dashboard UI locally

The dashboard is a single embedded page: `cloud-runner/internal/ui/index.html`. It is fully
self-contained (inline CSS + JS) and talks to the control plane over:

- `GET /api/v1/runs?limit=100`, `GET /api/v1/runs/{id}`
- `GET /api/v1/input-requests?status=open&limit=100` (Inbox badge/panel)
- `GET /api/v1/team/*` (Team view)
- `GET /events` — Server-Sent Events; each `data:` line is one JSON event. The `onmessage`
  handler renders the Activity feed and dispatches side effects (e.g. `mascotReact(parsed)`).

## Fastest realistic setup (no Railway, no auth)

Serving the file with `python3 -m http.server` works but leaves every panel in an error state
and gives you no SSE. Prefer a ~80-line fake control plane instead: a `ThreadingHTTPServer` that

1. serves `index.html` at `/`,
2. returns static JSON fixtures for the `/api/v1/...` endpoints you care about,
3. holds open `/events` as `text/event-stream`, writing `data: <json>\n\n` for anything POSTed
   to a custom `/inject` endpoint (plus periodic `: ping` comments to keep the socket alive).

Then drive real UI states from the shell:

```bash
curl -X POST http://127.0.0.1:8123/inject \
  -d '{"id":"e1","run_id":"run-1","type":"stage.failed","level":"error","message":"boom","created_at":"2026-01-01T00:00:00Z"}'
```

This is the only way to exercise SSE-driven behaviour (event feed, error/`run.completed`
handling) through the real code path instead of calling functions from the console.
Unused `/api/v1/team/*` routes returning 404 is fine — the Team view just shows the error text.

## Browser/GUI tips

- Maximize with `wmctrl -r :ACTIVE: -b add,maximized_vert,maximized_horz` (never `super+Up`).
- Narrow-viewport (`max-width:580px`) checks: unmaximize then
  `wmctrl -r :ACTIVE: -e 0,50,50,500,740` and reload; Chrome's minimum width lands around
  500px of viewport, which is enough to trip the media query.
- `prefers-reduced-motion` cannot be toggled from page JS: use DevTools →
  Ctrl+Shift+P → "Show Rendering" → "Emulate CSS media feature prefers-reduced-motion".
- To prove a CSS animation is actually running, capture two zoomed screenshots a moment apart
  and diff them with PIL (`ImageChops.difference(...).getbbox()`); `None` means identical
  (no animation), a bbox means it moved. Read `getComputedStyle(el).animationDuration` /
  `animationName` for the numeric claim (e.g. hover-speedup overrides).
- Page zoom via `ctrl+equal` (xdotool's `ctrl+plus` may not register) makes small widgets like
  the corner mascot legible in screenshots.
- Timed UI (auto-hide bubbles, periodic timers) can mask each other: the mascot re-quips every
  60s, so measure a 9s auto-hide inside a window that avoids the interval tick.

## Devin Secrets Needed

None — this setup is entirely local and unauthenticated.
