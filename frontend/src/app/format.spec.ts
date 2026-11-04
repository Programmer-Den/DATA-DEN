import { formatDuration, formatSize } from './format';

describe('formatSize', () => {
  it('picks a unit', () => {
    expect(formatSize(0)).toBe('0 B');
    expect(formatSize(1023)).toBe('1.023 B');
    expect(formatSize(1572864)).toBe('1,5 MB');
    expect(formatSize(4 * 1024 ** 4)).toBe('4 TB');
  });
});

describe('formatDuration', () => {
  const minute = 60000;

  it('shows the largest two units', () => {
    expect(formatDuration(0)).toBe('0 Min.');
    expect(formatDuration(59 * minute)).toBe('59 Min.');
    expect(formatDuration(90 * minute)).toBe('1 Std., 30 Min.');
    expect(formatDuration(1440 * minute + 5 * 60 * minute)).toBe('1 Tag, 5 Std.');
    expect(formatDuration(3 * 1440 * minute)).toBe('3 Tage, 0 Std.');
  });
});
