/** "Urlaub, 2026" as ["Urlaub", "2026"]: trimmed, without empty ones or repeats (ignoring case). */
export function parseTags(text: string): string[] {
  const tags: string[] = [];
  for (const part of text.split(',')) {
    const tag = part.trim();
    if (tag && !tags.some((have) => have.toLowerCase() === tag.toLowerCase())) {
      tags.push(tag);
    }
  }
  return tags;
}
