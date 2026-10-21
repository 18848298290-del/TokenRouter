// tailwind.config.js maps application design tokens to generated utility classes.
// Radius, overlay, motion, and panel-size utilities read the same CSS variables
// as handwritten styles. Color ramps and font stacks remain shared across pages,
// while dark edge intensity is configured separately from text and surface colors.

// darkEdgeColor creates a Tailwind color resolver for a configured edge alpha.
// The returned resolver multiplies opacityValue by that alpha, keeping modifiers
// such as /70 relative to the selected edge intensity rather than replacing it.
const darkEdgeColor =
  (opacity) =>
  ({ opacityValue = '1' }) =>
    `rgb(252 252 254 / calc(${opacity} * ${opacityValue}))`

// darkEdges supplies shared border, divide, and ring opacities while text and surface utilities use separate color ramps.
const darkEdges = {
  400: darkEdgeColor(0.3),
  500: darkEdgeColor(0.125),
  600: darkEdgeColor(0.078),
  700: darkEdgeColor(0.04),
  800: darkEdgeColor(0.031),
  900: darkEdgeColor(0.02),
}

/** @type {import('tailwindcss').Config} */
export default {
  content: ['./index.html', './src/**/*.{vue,js,ts,jsx,tsx}'],
  darkMode: 'class',
  theme: {
    // Radius utilities reference the same root variables as handwritten styles.
    // The UI check accepts these semantic tiers and rejects legacy numeric-scale names.
    borderRadius: {
      none: '0px',
      compact: 'var(--radius-compact)',
      control: 'var(--radius-control)',
      surface: 'var(--radius-surface)',
      dialog: 'var(--radius-dialog)',
      full: '9999px',
    },
    extend: {
      borderColor: { dark: darkEdges },
      divideColor: { dark: darkEdges },
      ringColor: { dark: darkEdges },
      opacity: {
        6: '0.06',
        8: '0.08',
      },
      // Overlay utilities reference root z-index variables. The tour's externally
      // constrained layer remains in its integration stylesheet instead of becoming
      // a utility that unrelated application panels could depend on.
      zIndex: {
        'chart-tooltip': 'var(--z-chart-tooltip)',
        'sidebar-overlay': 'var(--z-sidebar-overlay)',
        sidebar: 'var(--z-sidebar)',
        header: 'var(--z-header)',
        modal: 'var(--z-modal)',
        'modal-nested': 'var(--z-modal-nested)',
        tooltip: 'var(--z-tooltip)',
        announcement: 'var(--z-announcement)',
        'announcement-raised': 'var(--z-announcement-raised)',
        'announcement-top': 'var(--z-announcement-top)',
        'menu-overlay': 'var(--z-menu-overlay)',
        toast: 'var(--z-toast)',
        'action-menu': 'var(--z-action-menu)',
        'teleport-tooltip': 'var(--z-teleport-tooltip)',
        'help-tooltip': 'var(--z-help-tooltip)',
        'teleport-dropdown': 'var(--z-teleport-dropdown)',
      },
      // Panel height tiers reference root variables shared by menus, dialogs, and handwritten layout rules.
      maxHeight: {
        'menu-sm': 'var(--max-h-menu-sm)',
        menu: 'var(--max-h-menu)',
        panel: 'var(--max-h-panel)',
      },
      colors: {
        // Primary blue shades supply the application accent ramp across light and dark themes.
        primary: {
          50: '#F6FCFF',
          100: '#DDF4FC',
          200: '#BFEAFF',
          300: '#8BDDF8',
          400: '#4DD4F6',
          500: '#00D2FF',
          600: '#12A7E8',
          700: '#0B8FD8',
          800: '#176F9E',
          900: '#2D4F68',
          950: '#071A2A',
        },
        // Accent shades provide the secondary white-to-blue ramp used by branded surfaces.
        accent: {
          50: '#FFFFFF',
          100: '#EAF8FE',
          200: '#CDEFFD',
          300: '#9BDEFA',
          400: '#5BCDF2',
          500: '#1598D8',
          600: '#0B8FD8',
          700: '#176F9E',
          800: '#2D4F68',
          900: '#21465E',
          950: '#071A2A',
        },
        // Gray and slate utilities share neutral zinc values so reused utilities
        // stay in the same neutral family as the dark application surfaces.
        gray: {
          50: '#FAFAFA',
          100: '#F4F4F5',
          200: '#E4E4E7',
          300: '#D4D4D8',
          400: '#A1A1AA',
          500: '#71717A',
          600: '#52525B',
          700: '#3F3F46',
          800: '#27272A',
          900: '#18181B',
          950: '#09090B',
        },
        slate: {
          50: '#FAFAFA',
          100: '#F4F4F5',
          200: '#E4E4E7',
          300: '#D4D4D8',
          400: '#A1A1AA',
          500: '#71717A',
          600: '#52525B',
          700: '#3F3F46',
          800: '#27272A',
          900: '#18181B',
          950: '#09090B',
        },
        // @project-doc docs/architecture/frontend_ui_conventions.md#dark_colors
        // Dark text and surface colors use this ramp; border, divide, and ring intensity comes from darkEdges.
        dark: {
          50: '#FAFAFA', // Heading and emphasis text.
          100: '#DEE0E2', // Body, navigation, and header foreground.
          200: '#D4D4D8', // Secondary strong foreground.
          300: '#A1A1AA', // Secondary text foreground.
          400: '#8B8B94', // Hints and table headings.
          500: '#5F5F67', // Icons and disabled foreground.
          600: '#3D3D42', // Strong neutral fill.
          700: '#27272A', // Chips and disabled fills.
          800: '#17171A', // Row hover and nested surfaces.
          900: '#0F0F10', // Cards, dialogs, and dropdowns.
          950: '#141416', // Input, secondary-control, tab-track, and code surfaces.
        },
      },
      fontFamily: {
        // Latin text first selects the bundled Plus Jakarta Sans family. CJK glyphs
        // prefer an installed PingFang face, then the bundled Noto Sans SC face.
        sans: [
          '"Plus Jakarta Sans Variable"',
          '"PingFang SC"',
          '"Noto Sans SC Variable"',
          'system-ui',
          '-apple-system',
          'BlinkMacSystemFont',
          'Segoe UI',
          'Roboto',
          'Helvetica Neue',
          'Arial',
          'Hiragino Sans GB',
          'Microsoft YaHei',
          'sans-serif',
        ],
        mono: [
          '"Geist Mono Variable"',
          'ui-monospace',
          'SFMono-Regular',
          'Menlo',
          'Monaco',
          'Consolas',
          'monospace',
        ],
      },
      boxShadow: {
        // Structural surfaces use border separation; floating surfaces and branded
        // effects retain their explicitly selected shadow utilities.
        DEFAULT: 'none',
        sm: 'none',
        glass: 'none',
        'glass-sm': 'none',
        glow: '0 0 20px rgba(0, 210, 255, 0.28)',
        'glow-lg': '0 0 40px rgba(18, 167, 232, 0.35)',
        card: 'none',
        'card-hover': 'none',
        'inner-glow': 'inset 0 1px 0 rgba(255, 255, 255, 0.1)',
      },
      backgroundImage: {
        'gradient-radial': 'radial-gradient(var(--tw-gradient-stops))',
        'gradient-primary': 'linear-gradient(135deg, #00D2FF 0%, #0B8FD8 100%)',
        'gradient-dark': 'linear-gradient(135deg, #17171A 0%, #0A0A0B 100%)',
        'gradient-glass':
          'linear-gradient(135deg, rgba(255,255,255,0.1) 0%, rgba(255,255,255,0.05) 100%)',
        'mesh-gradient':
          'radial-gradient(at 40% 20%, rgba(0, 210, 255, 0.14) 0px, transparent 50%), radial-gradient(at 80% 0%, rgba(139, 221, 248, 0.12) 0px, transparent 50%), radial-gradient(at 0% 50%, rgba(18, 167, 232, 0.1) 0px, transparent 50%)',
      },
      // Transition utilities read the shared root timing variables also used by handwritten motion styles.
      transitionDuration: {
        DEFAULT: 'var(--motion-fast)',
        fast: 'var(--motion-fast)',
        normal: 'var(--motion-normal)',
        layout: 'var(--motion-layout)',
      },
      transitionTimingFunction: {
        DEFAULT: 'var(--motion-ease)',
        standard: 'var(--motion-ease)',
        exit: 'var(--motion-ease-exit)',
      },
      animation: {
        'fade-in': 'fadeIn 0.3s ease-out',
        'slide-up': 'slideUp 0.3s ease-out',
        'slide-down': 'slideDown 0.3s ease-out',
        'slide-in-right': 'slideInRight 0.3s ease-out',
        'scale-in': 'scaleIn 0.2s ease-out',
        'pulse-slow': 'pulse 3s cubic-bezier(0.4, 0, 0.6, 1) infinite',
        shimmer: 'shimmer 2s linear infinite',
        glow: 'glow 2s ease-in-out infinite alternate',
      },
      keyframes: {
        fadeIn: {
          '0%': { opacity: '0' },
          '100%': { opacity: '1' },
        },
        slideUp: {
          '0%': { opacity: '0', transform: 'translateY(10px)' },
          '100%': { opacity: '1', transform: 'translateY(0)' },
        },
        slideDown: {
          '0%': { opacity: '0', transform: 'translateY(-10px)' },
          '100%': { opacity: '1', transform: 'translateY(0)' },
        },
        slideInRight: {
          '0%': { opacity: '0', transform: 'translateX(20px)' },
          '100%': { opacity: '1', transform: 'translateX(0)' },
        },
        scaleIn: {
          '0%': { opacity: '0', transform: 'scale(0.95)' },
          '100%': { opacity: '1', transform: 'scale(1)' },
        },
        shimmer: {
          '0%': { backgroundPosition: '-200% 0' },
          '100%': { backgroundPosition: '200% 0' },
        },
        glow: {
          '0%': { boxShadow: '0 0 20px rgba(0, 210, 255, 0.28)' },
          '100%': { boxShadow: '0 0 30px rgba(18, 167, 232, 0.4)' },
        },
      },
      backdropBlur: {
        xs: '2px',
      },
    },
  },
  plugins: [],
}
