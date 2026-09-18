import React from 'react';
import {
  getThemePreset,
  SELECTABLE_THEME_PRESETS,
} from '@/components/Theme/themePresets';
import { useThemeStore } from '@/stores/useThemeStore';
import './ThemePicker.css';

interface ThemePickerProps {
  className?: string;
}

const ThemePicker: React.FC<ThemePickerProps> = ({ className = '' }) => {
  const theme = useThemeStore((state) => state.theme);
  const setTheme = useThemeStore((state) => state.setTheme);
  const currentTheme = getThemePreset(theme);

  return (
    <section className={`theme-picker ${className}`.trim()}>
      <div className="theme-picker__heading">
        <span>外观</span>
        <span className="theme-picker__current">{currentTheme.name}</span>
      </div>
      <div className="theme-picker__options" aria-label="选择主题">
        {SELECTABLE_THEME_PRESETS.map((preset) => {
          const selected = preset.id === theme;
          return (
            <label
              key={preset.id}
              className={`theme-picker__option${selected ? ' is-selected' : ''}`}
            >
              <input
                className="theme-picker__radio"
                type="radio"
                name="theme-picker"
                value={preset.id}
                checked={selected}
                onChange={() => setTheme(preset.id)}
              />
              <span
                className="theme-picker__swatch"
                aria-hidden="true"
                style={{
                  background: preset.colors.bgCard,
                  borderColor: preset.colors.borderLit,
                }}
              >
                <span style={{ background: preset.colors.primary }} />
              </span>
              <span className="theme-picker__label">{preset.name}</span>
            </label>
          );
        })}
      </div>
    </section>
  );
};

export default ThemePicker;
