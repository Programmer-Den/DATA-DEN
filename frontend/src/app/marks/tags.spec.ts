import { parseTags } from './tags';

describe('parseTags', () => {
  it('splits at commas, trims and drops empty tags and repeats', () => {
    expect(parseTags(' Urlaub, 2026,, urlaub ,Familie ')).toEqual(['Urlaub', '2026', 'Familie']);
  });

  it('gives no tags for an empty text', () => {
    expect(parseTags('  ')).toEqual([]);
  });
});
