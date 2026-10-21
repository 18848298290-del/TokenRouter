# Application routing

`index.ts` assembles route records, creates browser history, and installs navigation handlers. Record order is public, user, administrator, then the catch-all record. Route modules keep lazy component imports and metadata next to each destination.

| File | Responsibility |
| --- | --- |
| `routes/public.ts` | Setup, sign-in, callbacks, public destinations, and the separately appended catch-all record |
| `routes/user.ts` | User destinations, payment flows, and custom pages |
| `routes/admin.ts` | Administrator destinations and payment management |
| `guards.ts` | Access checks, successful-navigation prefetching, and chunk-load recovery |
| `navigationLoading.ts` | Progress identifiers and navigation completion |
| `title.ts` | Translated route titles, site names, and custom-menu labels |
| `setupRedirect.ts` | Destinations after installation is complete |
| `meta.d.ts` | Access and layout metadata declarations |

The module names organize route records. Each record's metadata determines access: payment result and SDK pages in the user module can still be public. Missing `requiresAuth` means authentication is required, including the catch-all route.

CLI import initialization removes one-time secrets before browser history captures its initial location. Navigation progress is installed before access guards. The first access check restores the browser session, resolves the document title, and then checks public access, authentication, administrator access, feature switches, and backend mode.

Payment, risk-control, team, ranking, and creative routes load public settings when necessary. They redirect when a loaded flag is false; unknown settings remain accessible. Affiliate routes require a true cached flag and do not initiate settings loading themselves. The backend validates authorization independently of these client-side checks.

`main.ts` prepares Pinia, injected settings, branding, and language before installing the router. It mounts the application after initial navigation resolves. `App.vue` keeps titles and branding synchronized after mounting and owns shared subscription and announcement refresh triggers.

The router suites exercise the installed production guard using registered route metadata. Run `pnpm exec vitest run src/router/__tests__ --maxWorkers=1` from `frontend/` for focused coverage of access decisions, secret cleanup order, titles, and navigation progress.
