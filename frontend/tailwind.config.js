/** @type {import('tailwindcss').Config} */
export default {
  content: ['./index.html', './src/**/*.{ts,tsx}'],
  // class 而非 media:值班台需要"我就要深色"这个显式选择,
  // 跟随系统只是三个选项之一(见 store/theme.ts)。
  darkMode: 'class',
  theme: {
    extend: {
      colors: {
        // 语义 token(定义见 src/index.css 的 :root.dark / :root.light)。
        // 全部走 token 而不是直接用 zinc-*/slate-*:硬编码色阶会让浅色主题
        // 需要逐个 class 覆盖 —— ongrid 就是这么做的,那份 CSS 里有 200 行
        // html.light 重映射。从一开始用 token 可以完全避开。
        bg: 'rgb(var(--bg) / <alpha-value>)',
        'bg-soft': 'rgb(var(--bg-soft) / <alpha-value>)',
        card: 'rgb(var(--card) / <alpha-value>)',
        'card-soft': 'rgb(var(--card-soft) / <alpha-value>)',
        line: 'rgb(var(--border) / <alpha-value>)',
        'line-soft': 'rgb(var(--border-soft) / <alpha-value>)',
        content: 'rgb(var(--text) / <alpha-value>)',
        muted: 'rgb(var(--text-muted) / <alpha-value>)',
        faint: 'rgb(var(--text-faint) / <alpha-value>)',
        accent: {
          DEFAULT: 'rgb(var(--accent) / <alpha-value>)',
          strong: 'rgb(var(--accent-strong) / <alpha-value>)',
          fg: 'rgb(var(--accent-fg) / <alpha-value>)',
        },
        ok: 'rgb(var(--ok) / <alpha-value>)',
        warn: 'rgb(var(--warn) / <alpha-value>)',
        danger: 'rgb(var(--danger) / <alpha-value>)',
        info: 'rgb(var(--info) / <alpha-value>)',
        p1: 'rgb(var(--p1) / <alpha-value>)',
        p2: 'rgb(var(--p2) / <alpha-value>)',
        p3: 'rgb(var(--p3) / <alpha-value>)',
        p4: 'rgb(var(--p4) / <alpha-value>)',
      },
      fontFamily: {
        mono: [
          'JetBrains Mono',
          'ui-monospace',
          'SFMono-Regular',
          'Menlo',
          'Consolas',
          'monospace',
        ],
      },
      fontSize: {
        // 值班台信息密度高,补两级比 text-xs 更小的字号用于元信息
        '2xs': ['0.6875rem', { lineHeight: '1rem' }],
      },
      animation: {
        'pulse-dot': 'pulse-dot 1.4s ease-in-out infinite',
      },
      keyframes: {
        'pulse-dot': {
          '0%, 80%, 100%': { opacity: '0.25' },
          '40%': { opacity: '1' },
        },
      },
    },
  },
  plugins: [],
}
