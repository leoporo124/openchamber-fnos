import type { GlobalThemeOverrides } from 'naive-ui'

const palettes = {
  light: {
    primary: '#b35017', hover: '#9a4310', active: '#85390c',
    background: '#fdfcfa', card: '#f7f6f4', elevated: '#f3f2f0',
    text: '#44443d', muted: '#696960', border: '#e5e1debf', borderHover: '#cbc7c2',
    selection: '#a9998f2b', interactive: '#0000000a', disabled: '#cbc7c2',
    success: '#517734', warning: '#886914', error: '#b7493f', info: '#2c6ebd'
  },
  dark: {
    primary: '#da7c47', hover: '#eb8c57', active: '#fd9b66',
    background: '#120f0e', card: '#1b1918', elevated: '#171616',
    text: '#dbd7cc', muted: '#89867c', border: '#242323bf', borderHover: '#504e4c',
    selection: '#c8c6c52b', interactive: '#ffffff0f', disabled: '#504e4c',
    success: '#76ad4f', warning: '#c67f13', error: '#da5b4a', info: '#479fe6'
  }
}

export function getThemeOverrides(theme: 'light' | 'dark'): GlobalThemeOverrides {
  const p = palettes[theme]
  return {
    common: {
      fontFamily: '-apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", sans-serif',
      borderRadius: '12px', borderRadiusSmall: '8px',
      primaryColor: p.primary, primaryColorHover: p.hover, primaryColorPressed: p.active, primaryColorSuppl: p.primary,
      successColor: p.success, successColorHover: p.success, successColorPressed: p.success,
      warningColor: p.warning, warningColorHover: p.warning, warningColorPressed: p.warning,
      errorColor: p.error, errorColorHover: p.error, errorColorPressed: p.error,
      infoColor: p.info, infoColorHover: p.info, infoColorPressed: p.info,
      bodyColor: p.background, cardColor: p.card, modalColor: p.card, popoverColor: p.card, tableColor: p.card,
      textColorBase: p.text, textColor1: p.text, textColor2: p.text, textColor3: p.muted,
      textColorDisabled: p.disabled, placeholderColor: p.muted,
      borderColor: p.border, dividerColor: p.border, hoverColor: p.interactive,
      inputColor: p.elevated, actionColor: p.elevated
    },
    Card: {
      borderRadius: '16px', borderColor: p.border, color: p.card,
      boxShadow: theme === 'dark'
        ? '0 1px 3px 0 rgba(0,0,0,0.3), 0 1px 2px -1px rgba(0,0,0,0.2)'
        : '0 1px 3px 0 rgba(0,0,0,0.04), 0 1px 2px -1px rgba(0,0,0,0.04)'
    },
    Button: { borderRadiusMedium: '10px', borderRadiusSmall: '8px', fontWeight: '500' },
    Input: {
      borderRadius: '10px', color: p.elevated, colorFocus: p.background,
      border: `1px solid ${p.border}`, borderHover: `1px solid ${p.borderHover}`, borderFocus: `1px solid ${p.primary}`
    },
    Tag: { borderRadius: '9999px' },
    Layout: { color: p.background, siderColor: p.card, headerColor: p.card, footerColor: p.card },
    Menu: {
      borderRadius: '10px', itemColorHover: p.interactive, itemColorActive: p.selection,
      itemTextColorActive: p.primary, itemIconColorActive: p.primary,
      itemTextColorActiveHover: p.hover, itemIconColorActiveHover: p.hover,
      itemTextColorHover: p.text, itemIconColorHover: p.primary
    },
    Switch: { railColorActive: p.primary },
    BackTop: { height: '32px', width: '32px', borderRadius: '16px', iconSize: '16px' }
  }
}

export const lightThemeOverrides = getThemeOverrides('light')
export const darkThemeOverrides = getThemeOverrides('dark')
