const sizeUnits = ['B', 'KB', 'MB', 'GB', 'TB'];
const number = new Intl.NumberFormat('de-DE', { maximumFractionDigits: 1 });

/** "1,5 MB" for 1572864. */
export function formatSize(bytes: number): string {
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < sizeUnits.length - 1) {
    value /= 1024;
    unit++;
  }
  return `${number.format(value)} ${sizeUnits[unit]}`;
}

/** "3 Tage, 4 Std." for a duration in milliseconds; minutes only under an hour. */
export function formatDuration(ms: number): string {
  const minutes = Math.max(0, Math.floor(ms / 60000));
  const days = Math.floor(minutes / 1440);
  const hours = Math.floor((minutes % 1440) / 60);
  if (days > 0) {
    return `${days} ${days === 1 ? 'Tag' : 'Tage'}, ${hours} Std.`;
  }
  if (hours > 0) {
    return `${hours} Std., ${minutes % 60} Min.`;
  }
  return `${minutes} Min.`;
}
