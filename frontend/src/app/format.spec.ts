import { formatSize } from './format';

describe('formatSize', () => {
  it('picks a unit', () => {
    expect(formatSize(0)).toBe('0 B');
    expect(formatSize(1023)).toBe('1.023 B');
    expect(formatSize(1572864)).toBe('1,5 MB');
    expect(formatSize(4 * 1024 ** 4)).toBe('4 TB');
  });
});
