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
  /** The town nearest to where a photo was taken. */
  place?: string;
  /** The ids of the people the server recognised in a photo (/api/people). */
  people?: string[];
  /** The things the server recognised in a photo, such as "Hunde". */
  labels?: string[];
  /** The shared folder the file is in, when it is another account's. */
  grant?: string;
  /** True when the editor can open the file (/api/text). */
  text?: boolean;
  /** The account's own tags of the file. */
  tags?: string[];
  /** True when the account marked it as a favourite. */
  favourite?: boolean;
}

/** When a photo was taken, or else when the file was last changed. */
export function takenAt(item: Item): Date {
  // Without a zone, Date reads it as local time, as the camera wrote it.
  return new Date(item.taken ?? item.modified);
}

/** The query of a file's address: its path and, in a shared folder, the grant. */
function query(path: string, grant?: string): string {
  return '?path=' + encodeURIComponent(path) + (grant ? '&grant=' + grant : '');
}

/** The file for the browser to show. */
export function viewUrl(path: string, grant?: string): string {
  return '/api/view' + query(path, grant);
}

/** A small JPEG of a photo for the gallery. */
export function thumbUrl(path: string, grant?: string): string {
  return '/api/thumb' + query(path, grant);
}

/** The file to save. */
export function downloadUrl(path: string, grant?: string): string {
  return '/api/download' + query(path, grant);
}

/** True for a PDF; other previewable documents are plain text. */
export function isPdf(item: Item): boolean {
  return item.name.toLowerCase().endsWith('.pdf');
}
