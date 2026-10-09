# headcount1 landing page

Static marketing page, built with a dependency-free Node script and hosted on Cloudflare.

- `build.mjs` holds the page content and markup and writes `dist/`. The app and GitHub links are the `APP_URL` and `GITHUB_URL` constants at the top.
- `src/legal.mjs` holds the Terms of Service and Privacy Policy, emitted as `dist/terms.html` and `dist/privacy.html` (served at `/terms` and `/privacy`). The company name and details, country, contact email and effective date are constants at the top.
- `src/styles.css` is inlined into every page; `src/main.js` is emitted as a content-hashed file under `dist/assets/`.
- `public/` is copied to `dist/` as is (`_headers`, `robots.txt`, favicon).

## Build

```sh
node build.mjs
```

## Deploy to Cloudflare

The site is deployed as a Cloudflare Worker that serves static assets only. `wrangler.toml` sets the project name (`headcount1-landing`) and points the assets at `dist/`.

Git integration (Workers & Pages → Create → Import a repository):

| Setting | Value |
| --- | --- |
| Project name | `headcount1-landing` (must match `wrangler.toml`) |
| Build command | `node build.mjs` |
| Deploy command | `npx wrangler deploy` |
| Path (Advanced settings) | `/landing` |

Or deploy from your machine:

```sh
npm run deploy
```

`public/_headers` sets security headers, including a Content-Security-Policy that allows only same-origin scripts and Google Fonts, and long-lived caching for `/assets/*`.
