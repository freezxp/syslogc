# Session usage

Claude Code session `a4baae1a-ced8-4020-9480-6cf6a43a4ce4`, ending 2026-09-14. It covered planning and Phases 0–4.

## Token usage

Summed from the session's saved conversation logs. Streamed replies share one message ID and are counted once.

| | API requests | Output tokens | Tokens written to cache | Tokens read from cache | Uncached input |
|---|---:|---:|---:|---:|---:|
| Main conversation | 422 | 782,467 | 1,363,274 | 153,602,149 | 842 |
| Frontend UI agent | 69 | 187,596 | 1,232,869 | 40,125,039 | 140 |
| **Total** | **491** | **970,063** | **2,596,143** | **193,727,188** | **982** |

- About 197 million tokens in all; 98% are cache reads, where each request re-reads the conversation from cache at a lower price than new input.
- Model: Claude Opus 5 for every request.
- Credits and cost are not computed here, because plan pricing is not visible from inside the session. `/usage` in Claude Code shows the actual billing numbers.

## Lines created

Every commit in the repository (57 commits) was authored in this session.

- Git reports 46,142 lines added and 729 deleted across all branches, excluding merge commits.
- That figure includes about 9,000 generated lines (`frontend/package-lock.json` at 7,193 lines and `frontend/src/api/schema.d.ts` at 1,801), plus `go.sum` and fuzz test data.

Lines currently in the repository, excluding lockfiles, generated API types, `go.sum`, test data and images:

| Category | Lines |
|---|---:|
| Go (production) | 13,241 |
| Go (tests) | 5,242 |
| TypeScript/TSX (production) | 7,637 |
| TypeScript (tests and mocks) | 1,357 |
| Docs (Markdown) | 5,600 |
| OpenAPI, YAML, config | 1,434 |
| Shell, Makefile, Docker, CI | 700 |
| SQL | 105 |
| Other (CSS, JSON, HTML, JS) | 1,006 |
| **Total** | **36,322** |

Counts are raw lines, including comments and blank lines. The frontend UI agent wrote most of the TypeScript; everything else came from the main conversation.
