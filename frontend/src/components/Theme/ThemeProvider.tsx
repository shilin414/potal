import React, { useEffect, useMemo } from 'react';
import { ConfigProvider, theme as antTheme } from 'antd';
import { useThemeStore } from '@/stores/useThemeStore';

const darkToken = {
  colorPrimary: '#E8A838',
  colorBgContainer: '#1C1C28',
  colorBgElevated: '#242434',
  colorBgLayout: '#0C0C11',
  colorBorder: '#2C2C3C',
  colorBorderSecondary: '#3A3A4E',
  colorText: '#EAEAF0',
  colorTextSecondary: '#9898A8',
  colorTextTertiary: '#5C5C6E',
  colorTextQuaternary: '#3C3C4E',
  colorFill: '#2C2C3C',
  colorFillSecondary: '#242434',
  colorFillTertiary: '#1C1C28',
  colorFillQuaternary: '#14141C',
  borderRadius: 10,
  fontFamily: "'Noto Sans SC', 'Space Grotesk', sans-serif",
};

const lightToken = {
  colorPrimary: '#D97706',
  colorBgContainer: '#FFFFFF',
  colorBgElevated: '#F1F3F5',
  colorBgLayout: '#F8F9FA',
  colorBorder: '#E5E7EB',
  colorBorderSecondary: '#D1D5DB',
  colorText: '#1F2937',
  colorTextSecondary: '#6B7280',
  colorTextTertiary: '#9CA3AF',
  colorTextQuaternary: '#D1D5DB',
  colorFill: '#F1F3F5',
  colorFillSecondary: '#F8F9FA',
  colorFillTertiary: '#FFFFFF',
  colorFillQuaternary: '#FFFFFF',
  borderRadius: 10,
  fontFamily: "'Noto Sans SC', 'Space Grotesk', sans-serif",
};

interface ThemeProviderProps {
  children: React.ReactNode;
}

const ThemeProvider: React.FC<ThemeProviderProps> = ({ children }) => {
  const theme = useThemeStore((s) => s.theme);

  useEffect(() => {
    const root = document.documentElement;
    if (theme === 'dark') {
      root.classList.add('dark');
    } else {
      root.classList.remove('dark');
    }
    root.setAttribute('data-theme', theme);
  }, [theme]);

  const antdThemeConfig = useMemo(() => {
    const isDark = theme === 'dark';
    return {
      algorithm: isDark ? antTheme.darkAlgorithm : antTheme.defaultAlgorithm,
      token: isDark ? darkToken : lightToken,
      components: {
        Layout: {
          headerBg: isDark ? '#14141C' : '#FFFFFF',
          siderBg: isDark ? '#14141C' : '#FFFFFF',
          bodyBg: isDark ? '#0C0C11' : '#F8F9FA',
        },
        Menu: {
          darkItemBg: '#14141C',
          darkSubMenuItemBg: '#0C0C11',
          itemBg: '#FFFFFF',
        },
        Card: {
          colorBgContainer: isDark ? '#1C1C28' : '#FFFFFF',
        },
        Modal: {
          contentBg: isDark ? '#1C1C28' : '#FFFFFF',
          headerBg: isDark ? '#1C1C28' : '#FFFFFF',
        },
        Input: {
          colorBgContainer: isDark ? '#242434' : '#FFFFFF',
        },
        Button: {
          colorPrimary: isDark ? '#E8A838' : '#D97706',
          algorithm: true,
        },
      },
    };
  }, [theme]);

  return (
    <ConfigProvider theme={antdThemeConfig}>
      {children}
    </ConfigProvider>
  );
};

export default ThemeProvider;
