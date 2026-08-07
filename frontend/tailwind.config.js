/** @type {import('tailwindcss').Config} */
export default {
  content: [
    "./index.html",
    "./src/**/*.{vue,ts,tsx}",
  ],
  darkMode: "class",
  theme: {
    extend: {
      colors: {
        ink: {
          DEFAULT: "#0c0d0b",
          deep: "#060706",
        },
        surface: {
          0: "#111310",
          1: "#171a15",
          2: "#1f231b",
          3: "#272b24",
        },
        accent: {
          DEFAULT: "#f0a500",
          soft: "rgba(240,165,0,0.14)",
          glow: "rgba(240,165,0,0.35)",
        },
        content: {
          DEFAULT: "#edede8",
          muted: "#a8aba3",
          faint: "#6d7068",
        },
        ok: "#34d399",
        warn: "#fbbf24",
        danger: "#f87171",
      },
      fontFamily: {
        sans: ["Inter", "system-ui", "sans-serif"],
        mono: ["ui-monospace", "SFMono-Regular", "Menlo", "monospace"],
      },
      borderRadius: {
        sm: "8px",
        DEFAULT: "12px",
        lg: "16px",
        xl: "20px",
      },
      keyframes: {
        fadeIn: { "0%": { opacity: "0" }, "100%": { opacity: "1" } },
        slideUp: { "0%": { transform: "translateY(8px)", opacity: "0" }, "100%": { transform: "translateY(0)", opacity: "1" } },
        shimmer: { "0%": { backgroundPosition: "-200% 0" }, "100%": { backgroundPosition: "200% 0" } },
        pulse: { "0%,100%": { opacity: "1" }, "50%": { opacity: "0.4" } },
      },
      animation: {
        fadeIn: "fadeIn 0.2s ease-out",
        slideUp: "slideUp 0.25s ease-out",
        shimmer: "shimmer 1.5s linear infinite",
        pulse: "pulse 1.6s ease-in-out infinite",
      },
    },
  },
  plugins: [],
}
