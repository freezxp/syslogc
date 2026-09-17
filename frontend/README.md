# Syslogc web UI

React 19 + TypeScript single-page app for Syslogc. The production build in `dist/`
is embedded into the Go binary (`make web`). UX and architecture: `docs/frontend.md`.

## Scripts

| Command                               |                                                                                                                                                                       |
| ------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `npm run dev`                         | Vite on :5173, proxying `/api`, `/health`, `/ready`, `/metrics` to `SYSLOGC_BACKEND` (default `http://127.0.0.1:8080`)                                                |
| `npm run dev:mock`                    | Full demo without a backend: MSW mocks every endpoint with synthetic logs and a mock live tail (any username; password `wrong` fails)                                 |
| `npm run build`                       | Type check and build to `dist/`                                                                                                                                       |
| `npm run gen:api`                     | Regenerate `src/api/schema.d.ts` from `../docs/openapi.yaml`                                                                                                          |
| `npm run lint` / `typecheck` / `test` | ESLint + Prettier, `tsc`, Vitest                                                                                                                                      |
| `npm run screenshots`                 | Playwright captures of the mock app into `docs/screenshots/` (fails on console errors; `BASE_URL=…` to use a running server; needs `npx playwright install chromium`) |

## Layout

```text
src/
├── api/            # generated schema, openapi-fetch client (CSRF, 401 handling), TanStack Query hooks
├── app/            # router, app shell, command palette, shortcuts help
├── components/     # ui/ (Radix-based primitives), data/ (badges, panels), charts/ (Recharts wrappers)
├── features/       # auth, dashboard, explorer, live-tail, saved-searches, system, settings, time-range
├── lib/            # filter text parser/formatter, time ranges, URL state, ring buffer, preferences
├── mocks/          # MSW handlers and synthetic data (only bundled in mock mode)
└── styles/         # Tailwind v4 tokens (dark default, light theme)
```

## Notes

- Explorer state lives in the URL (`from,to,tz,q,native,mode,cols,split`); share links reproduce the view.
  Relative ranges are resolved to absolute timestamps once per run so pagination stays consistent.
- The filter text form (`hostname=fw01 AND severity in (error, critical)`) is a lossless representation of the
  API filter AST; the grammar is documented in `src/lib/filter-text.ts` and vectors in `filter-text.vectors.json`.
- Live tail uses `EventSource` on `/api/v1/logs/tail?q=<base64url JSON>` with `logs`, `stats` and `error` events,
  a bounded ring buffer and requestAnimationFrame batching.
- Exports stream to disk with the File System Access API when available, otherwise fall back to a blob or a
  form POST (`request` JSON + `csrf_token` fields).
- No `dangerouslySetInnerHTML` (enforced by ESLint); log values are rendered as text only.
