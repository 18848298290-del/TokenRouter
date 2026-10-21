# Shared application state

These Pinia stores retain state across routed components. Install Pinia before requesting a store; the barrel exports store factories, while domain types come from their owning API or UI modules.

| Store | Responsibility |
| --- | --- |
| `auth.ts` | Authenticated identity, pending external sign-in, browser persistence, profile polling, and token renewal |
| `app.ts` | Sidebar state, reference-counted loading, notifications, public settings, and release information |
| `adminSettings.ts` | Cached administrator navigation switches and custom menus |
| `subscriptions.ts` | Active subscription cache, request publication, and polling |
| `announcements.ts` | Visible announcements, fetch throttling, and popup playback |
| `payment.ts` | Payment configuration, normalized plan choices, and caller-selected order state |
| `onboarding.ts` | Shared tour driver and component-owned control callbacks |

The router restores authentication once during its first access check. `main.ts` applies injected public configuration before initial navigation, then checks its locale after language initialization. The application shell starts subscription polling and announcement refreshes for authenticated users and clears their local state when authentication ends.

Public-settings requests share one active promise, including forced calls. When idle, the cache is reused only for its stored locale. Subscription requests additionally use a generation counter to prevent superseded responses from publishing. Announcement reads use a twenty-minute ordinary-fetch window; forced calls bypass it.

Authentication storage contains the access token, serialized user, refresh token, absolute token expiry, and optional pending authentication summary. Local logout stops owned timers and clears those entries. The stores' individual action comments describe failure behavior and the lifecycle of requests already in flight.

Run `pnpm exec vitest run src/stores/__tests__ --maxWorkers=1` from `frontend/` for focused store coverage. Full frontend validation remains part of the repository verification workflow.
