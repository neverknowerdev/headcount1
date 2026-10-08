# headcount1 landing page

Static marketing page, built with a dependency-free Node script and hosted on Cloudflare Pages.

- `build.mjs` holds the page content and markup and writes `dist/`. The app and GitHub links are the `APP_URL` and `GITHUB_URL` constants at the top.
- `src/styles.css` is inlined into the page; `src/main.js` is emitted as a content-hashed file under `dist/assets/`.
- `public/` is copied to `dist/` as is (`_headers`, `robots.txt`, favicon).

## Build

```sh
node build.mjs
```

## Deploy to Cloudflare Pages

Git integration (Workers & Pages → Create → Pages → Connect to Git):

| Setting | Value |
| --- | --- |
| Root directory | `landing` |
| Build command | `node build.mjs` |
| Build output directory | `dist` |

Or deploy from your machine:

```sh
npm run deploy
```

`wrangler.toml` sets the project name (`headcount1-landing`) and output directory. `public/_headers` sets security headers, including a Content-Security-Policy that allows only same-origin scripts and Google Fonts, and long-lived caching for `/assets/*`.
