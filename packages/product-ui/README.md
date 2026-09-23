# `@aoagents/product-ui`

Portable AO product presentation models, pure formatting helpers, and reusable
React leaf components for desktop and cloud clients.

## Boundary

This package intentionally does not know about generated API clients, Electron,
the AO daemon, renderer stores, or any application's i18n singleton. Consumers
adapt wire data into the exported neutral models and inject translated labels or
asset URLs at their application boundary.

```tsx
import { AgentAvatar, getSessionStatusView } from "@aoagents/product-ui";

const status = getSessionStatusView(session.status, (key) => t(key));

<AgentAvatar
	provider={session.provider}
	logoSources={{ "claude-code": claudeLogo }}
/>;
```

The package ships JavaScript and declarations in `dist`. Tailwind consumers
should include `@aoagents/product-ui/dist` in their source scan because the
components use AO design-system utility classes and semantic tokens.

`SessionsBoardGridView`, `SessionCardView`, and `SessionsArchiveView` accept
neutral presentation models plus focused action/asset slots. Hosts retain data
fetching, routing, persistence, and platform-specific controls.

Project exports provide controlled setup and settings presentations, neutral
project models, and pure validation. Hosts inject translated labels and their
own agent, model, reviewer, intake, persistence, and platform actions.

## Development

```bash
npm install
npm run typecheck
npm test
npm run build
```
