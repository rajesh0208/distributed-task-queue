/**
 * tailwind.config.js
 *
 * Tailwind CSS v3 configuration for the distributed task queue frontend.
 *
 * # content (purge paths)
 *   Tailwind scans these files at build time to find every class name used in
 *   the project. Any class NOT found in these files is removed from the
 *   production CSS bundle (tree-shaking). Vite handles this automatically when
 *   building with `vite build`.
 *
 *   "./index.html"         — the root HTML template (rarely has Tailwind classes,
 *                            but included for completeness)
 *   "./src/**\/*.{js,jsx}" — all component, page, and utility files; the glob
 *                            picks up nested directories automatically
 *
 * # theme.extend
 *   Empty for now — all styling uses Tailwind's built-in design tokens
 *   (slate-*, indigo-*, emerald-*, etc.). Custom tokens (brand colours,
 *   spacing overrides) would go here without replacing the defaults.
 *
 * # plugins
 *   No official or community plugins installed. Candidates for future addition:
 *     @tailwindcss/forms    — normalises form element styles (useful for
 *                             the login form and submit inputs)
 *     @tailwindcss/typography — prose class for markdown/rich-text rendering
 */

/** @type {import('tailwindcss').Config} */
export default {
  // Scan all HTML and JS/JSX source files so unused utility classes are
  // purged from the production bundle.
  content: [
    "./index.html",
    "./src/**/*.{js,jsx}",
  ],
  theme: {
    extend: {},  // no custom tokens yet; add brand colours / spacing here
  },
  plugins: [],  // no plugins; @tailwindcss/forms would help normalise inputs
}

