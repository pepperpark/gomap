# gomap docs (GitHub Pages)

This folder contains a lightweight, developer‑focused single‑page website for gomap. It is designed to be served by GitHub Pages directly from the `/docs` directory.

## Structure

- `index.html` — the main single page
- `styles.css` — modern light/dark theme styles
- `script.js` — dark mode toggle and copy‑to‑clipboard helpers
- `logo.svg` — tiny vector logo used in the header

No build step is required.

## Enable GitHub Pages

1. Open your repository on GitHub: https://github.com/pepperpark/gomap
2. Go to Settings → Pages
3. Set:
   - Source: **Deploy from a branch**
   - Branch: **main** (or your default) / **/docs** folder
4. Save. Your site will be available shortly at:
   - `https://pepperpark.github.io/gomap/`

## Local preview (optional)
You can open `docs/index.html` directly in your browser or serve it locally with any static server:

```bash
python3 -m http.server -d docs 8080
# then open http://localhost:8080/
```

## Links
- Project: https://github.com/pepperpark/gomap
- Releases: https://github.com/pepperpark/gomap/releases
- Issues: https://github.com/pepperpark/gomap/issues
