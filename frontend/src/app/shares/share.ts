/** A link to a file or folder, as /api/shares lists it. */
export interface Share {
  id: string;
  /** What is shared, such as "/Fotos/2026". */
  path: string;
  name: string;
  dir: boolean;
  created: string;
  expires: string;
  /** True when the link asks for a password. */
  password: boolean;
  /** True for an upload link: others drop files in, but see nothing. */
  upload?: boolean;
  /** True when the file or folder is gone, for example in the recycle bin. */
  missing?: boolean;
}

/** How long a new link works, in days. */
export const shareDays: { days: number; name: string }[] = [
  { days: 1, name: '1 Tag' },
  { days: 7, name: '7 Tage' },
  { days: 30, name: '30 Tage' },
  { days: 90, name: '90 Tage' },
  { days: 365, name: '1 Jahr' },
];

/** The address to send someone, such as "https://nas.example/s/0a1b…". */
export function shareLink(id: string): string {
  return `${location.origin}/s/${id}`;
}

/** Copies text to the clipboard; false when the browser does not allow it. */
export async function copy(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    return false;
  }
}
