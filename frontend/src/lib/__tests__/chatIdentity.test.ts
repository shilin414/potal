import { describe, expect, it } from 'vitest';
import {
  agentAvatarFallback,
  agentAvatarUrl,
  agentDisplayName,
  formatUserLabel,
  userAvatarFallback,
  userAvatarUrl,
  userDisplayId,
  userDisplayName,
} from '../chatIdentity';

describe('user identity label', () => {
  it('renders 姓名（user_id） for a Feishu user', () => {
    expect(formatUserLabel({
      username: '吴志彬',
      display_name: '吴志彬',
      display_id: '19127920',
    })).toBe('吴志彬（19127920）');
  });

  it('accepts a numeric display_id', () => {
    expect(formatUserLabel({ display_name: '吴志彬', display_id: 19127920 }))
      .toBe('吴志彬（19127920）');
  });

  it('prefers display_name over username', () => {
    expect(userDisplayName({ username: 'wuzhibin', display_name: '吴志彬' }))
      .toBe('吴志彬');
    expect(userDisplayName({ username: 'wuzhibin' })).toBe('wuzhibin');
  });

  it('falls back to 我 for an anonymous payload', () => {
    expect(userDisplayName(null)).toBe('我');
    expect(userDisplayName({})).toBe('我');
  });

  it('drops the brackets when no id is known', () => {
    expect(formatUserLabel({ display_name: '吴志彬' })).toBe('吴志彬');
    expect(userDisplayId({ display_id: '' })).toBe('');
    expect(userDisplayId({ display_id: null })).toBe('');
  });

  it('trims whitespace-only values', () => {
    expect(formatUserLabel({ display_name: '  ', display_id: ' 7 ' })).toBe('我（7）');
  });
});

describe('user avatar', () => {
  it('prefers avatar_url over avatar', () => {
    expect(userAvatarUrl({ avatar: 'a.png', avatar_url: 'b.png' })).toBe('b.png');
    expect(userAvatarUrl({ avatar: 'a.png' })).toBe('a.png');
    expect(userAvatarUrl({})).toBe('');
  });

  it('uses the first character as fallback content', () => {
    expect(userAvatarFallback({ display_name: '吴志彬' })).toBe('吴');
    expect(userAvatarFallback({ username: 'demo' })).toBe('D');
    expect(userAvatarFallback({})).toBe('我');
  });
});

describe('agent identity', () => {
  it('uses the application name, never the generic 助手', () => {
    expect(agentDisplayName({ name: '创作助手' })).toBe('创作助手');
    expect(agentDisplayName({})).toBe('智能体');
    expect(agentDisplayName(null)).toBe('智能体');
  });

  it('falls back to the emoji icon before the AI placeholder', () => {
    expect(agentAvatarFallback({ icon: '🤖' })).toBe('🤖');
    expect(agentAvatarFallback({ icon: '' })).toBe('AI');
    expect(agentAvatarFallback(undefined)).toBe('AI');
  });

  it('exposes the uploaded avatar url when present', () => {
    expect(agentAvatarUrl({ avatar_url: '/api/v2/applications/2/avatar?v=1' }))
      .toBe('/api/v2/applications/2/avatar?v=1');
    expect(agentAvatarUrl({ icon: '✨' })).toBe('');
  });
});
