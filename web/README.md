# GhostFleet Web UI

The controller's single-page app: Vue 3 + TypeScript + Vite + Tailwind CSS.
The production build (`dist/`) is served by the controller itself — there is
no frontend container and no Node at runtime. Live views (runs, agent status,
console) poll the JSON API; charts are hand-rolled inline SVG sparklines.

```sh
npm ci             # install
npm run dev        # dev server; proxies /api to a controller on :8080
npm run build      # production build into dist/ (what `make build` uses)
npx vue-tsc -b     # type-check (part of `make test`)
```
