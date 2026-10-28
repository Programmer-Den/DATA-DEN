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
