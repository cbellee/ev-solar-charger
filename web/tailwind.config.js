/** @type {import('tailwindcss').Config} */
export default {
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  theme: {
    extend: {
      colors: {
        // Custom shade matching the legacy template's `bg-gray-750`.
        "gray-750": "#2d3744",
      },
    },
  },
  plugins: [],
};
