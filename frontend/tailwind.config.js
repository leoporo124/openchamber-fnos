const color = (name) => `rgb(var(--oc-${name}) / <alpha-value>)`
const accent = (name) => Object.fromEntries([50, 100, 200, 300, 400, 500, 600, 700, 800, 900, 950]
  .map((shade) => [shade, color(shade <= 100 || shade >= 900 ? `${name}-muted` : name)]))
const solid = (name) => Object.fromEntries([50, 100, 200, 300, 400, 500, 600, 700, 800, 900, 950]
  .map((shade) => [shade, color(name)]))

/** @type {import('tailwindcss').Config} */
export default {
  darkMode: 'class',
  content: [
    "./index.html",
    "./src/**/*.{vue,js,ts,jsx,tsx}",
  ],
  theme: {
    extend: {
      textColor: {
        slate: {
          50: color('text'), 100: color('text'), 200: color('text'),
          300: color('muted'), 400: color('muted'), 500: color('muted'), 600: color('muted'),
          700: color('text'), 800: color('text'), 900: color('text'), 950: color('text')
        },
        blue: solid('primary'), purple: solid('primary'),
        emerald: solid('success'), rose: solid('error'), amber: solid('warning')
      },
      borderColor: { slate: solid('border') },
      colors: {
        slate: {
          50: color('surface'), 100: color('surface'), 200: color('border'),
          300: color('muted'), 400: color('muted'), 500: color('muted'), 600: color('muted'),
          700: color('text'), 800: color('text'), 900: color('text'), 950: color('text')
        },
        blue: accent('primary'), purple: accent('primary'),
        emerald: accent('success'), rose: accent('error'), amber: accent('warning'),
        fnos: {
          blue: color('primary'), 'blue-hover': color('primary-hover'),
          bg: color('background'), 'bg-dark': color('background'),
          sidebar: color('surface'), 'sidebar-dark': color('surface'),
          card: color('surface'), 'card-dark': color('surface')
        }
      }
    },
  },
  plugins: [],
}
