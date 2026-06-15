/** @type {import('tailwindcss').Config} */
export default {
  // System-aware dark mode; a later prompt can switch to 'class' for a toggle.
  darkMode: 'media',
  content: ['./index.html', './src/**/*.{ts,tsx}'],
  theme: {
    extend: {
      fontFamily: {
        sans: ['Inter', 'ui-sans-serif', 'system-ui', 'sans-serif'],
        mono: ['"IBM Plex Mono"', 'ui-monospace', 'SFMono-Regular', 'monospace'],
      },
      // Status semantics (spec §5.4): small dot + text, used sparingly.
      colors: {
        status: {
          running: '#1D9E75',
          stopped: '#6B7280',
          exited: '#6B7280',
          warn: '#BA7517',
          danger: '#DC2626',
        },
      },
      fontSize: {
        '2xs': ['11px', '14px'],
      },
      borderWidth: {
        hairline: '0.5px',
      },
    },
  },
  plugins: [],
}
