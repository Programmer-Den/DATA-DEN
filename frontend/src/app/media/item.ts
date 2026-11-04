/** The views a file can belong to; the server decides by its extension. */
export type Kind = 'photo' | 'video' | 'audio' | 'document';

/** A file with its path from the top, such as "/Fotos/2026/a.jpg". */
export interface Item {
  path: string;
  name: string;
  size: number;
  modified: string;
  kind?: Kind;
  /** True when the browser can show the file (/api/view). */
  preview?: boolean;
  /** True when the server makes a small thumbnail of the photo (/api/thumb). */
  thumb?: boolean;
  /** When a photo was taken, in the camera's local time without a zone. */
  taken?: string;
  /** Where a photo was taken. */
  lat?: number;
  lon?: number;
}

/** When a photo was taken, or else when the file was last changed. */
export function takenAt(item: Item): Date {
  // Without a zone, Date reads it as local time, as the camera wrote it.
  return new Date(item.taken ?? item.modified);
}

/** The file for the browser to show. */
export function viewUrl(path: string): string {
  return '/api/view?path=' + encodeURIComponent(path);
}

/** A small JPEG of a photo for the gallery. */
export function thumbUrl(path: string): string {
  return '/api/thumb?path=' + encodeURIComponent(path);
}

/** The file to save. */
export function downloadUrl(path: string): string {
  return '/api/download?path=' + encodeURIComponent(path);
}

/** True for a PDF; other previewable documents are plain text. */
export function isPdf(item: Item): boolean {
  return item.name.toLowerCase().endsWith('.pdf');
}
