/** @type {import('tailwindcss').Config} */
export default {
  content: ["./index.html", "./src/**/*.{vue,js,ts,jsx,tsx}"],
  theme: {
    extend: {
      colors: {
        ink: {
          950: "#0b0c10",
          900: "#11131a",
          800: "#1a1d29",
          700: "#262a38",
          600: "#3a4055",
          500: "#5b6379",
          400: "#8a93ac",
          300: "#aeb6cc",
          200: "#cfd5e6",
          100: "#e7ecf6",
        },
        accent: {
          DEFAULT: "#7c5cff",
          50: "#f1edff",
          100: "#dccfff",
          500: "#7c5cff",
          600: "#6a48ee",
          700: "#5536d6",
        },
      },
      fontFamily: {
        sans: [
          "-apple-system",
          "BlinkMacSystemFont",
          "Segoe UI",
          "PingFang SC",
          "Hiragino Sans GB",
          "Microsoft YaHei",
          "sans-serif",
        ],
      },
    },
  },
  plugins: [],
};
