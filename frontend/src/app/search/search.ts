import { HttpClient } from '@angular/common/http';
import { Component, computed, effect, inject, input, output, signal } from '@angular/core';
import { errorMessage } from '../auth.service';
import { formatSize } from '../format';
import { Item } from '../media/item';
import { Viewer } from '../media/viewer';

/** A search result: a file or folder, with text around the hit in a document. */
export interface Hit extends Item {
  dir: boolean;
  snippet?: string;
}

interface Results {
  items: Hit[];
  total: number;
  /** False while the server still builds its index after a start. */
  ready: boolean;
  /** How many scans and images still wait for the text recognition. */
  recognizing?: number;
}

/** A piece of text, marked when it is one of the words searched for. */
export interface Part {
  text: string;
  hit: boolean;
}

/** Cuts text into parts, marking every place a word occurs, ignoring case. */
export function mark(text: string, words: string[]): Part[] {
  const lower = text.toLowerCase();
  const marked = new Array<boolean>(text.length).fill(false);
  for (const word of words) {
    // Lowercasing must keep the length, else the places would shift.
    if (!word || lower.length !== text.length) {
      continue;
    }
    for (let i = lower.indexOf(word); i >= 0; i = lower.indexOf(word, i + word.length)) {
      marked.fill(true, i, i + word.length);
    }
  }
  const parts: Part[] = [];
  for (let i = 0; i < text.length; i++) {
    const last = parts.at(-1);
    if (last && last.hit === marked[i]) {
      last.text += text[i];
    } else {
      parts.push({ text: text[i], hit: marked[i] });
    }
  }
  return parts;
}

const date = new Intl.DateTimeFormat('de-DE', { dateStyle: 'medium' });

/** Files and folders found by name, path or the text of documents. */
@Component({
  selector: 'app-search',
  imports: [Viewer],
  templateUrl: './search.html',
  styleUrl: './search.css',
})
export class SearchView {
  private readonly http = inject(HttpClient);

  readonly query = input.required<string>();
  /** Asks to show a folder in the folder view. */
  readonly openFolder = output<string>();

  protected readonly results = signal<Results | null>(null);
  protected readonly error = signal<string | null>(null);
  /** The query without spaces around it. */
  protected readonly term = computed(() => this.query().trim());
  protected readonly words = computed(() => this.term().toLowerCase().split(/\s+/).filter(Boolean));
  /** The hits the viewer can show, to go through with the arrow keys. */
  protected readonly previews = computed(() =>
    (this.results()?.items ?? []).filter((hit) => !hit.dir && hit.preview),
  );
  /** The hit open in the viewer, an index into previews, or null. */
  protected readonly viewing = signal<number | null>(null);
  protected readonly formatSize = formatSize;
  protected readonly mark = mark;

  constructor() {
    effect((onCleanup) => {
      const q = this.term();
      this.results.set(null);
      this.error.set(null);
      this.viewing.set(null);
      if (!q) {
        return;
      }
      const sub = this.http.get<Results>('/api/search', { params: { q } }).subscribe({
        next: (results) => this.results.set(results),
        error: (err: unknown) => this.error.set(errorMessage(err)),
      });
      onCleanup(() => sub.unsubscribe());
    });
  }

  /** "/Fotos/2026" for "/Fotos/2026/a.jpg". */
  protected folder(hit: Hit): string {
    return hit.path.slice(0, -hit.name.length - 1) || '/';
  }

  protected date(hit: Hit): string {
    return date.format(new Date(hit.modified));
  }

  protected icon(hit: Hit): string {
    if (hit.dir) {
      return '📁';
    }
    switch (hit.kind) {
      case 'photo':
        return '🖼';
      case 'video':
        return '🎬';
      case 'audio':
        return '🎵';
      case 'document':
        return '📄';
      default:
        return '📎';
    }
  }

  protected open(hit: Hit): void {
    if (hit.dir) {
      this.openFolder.emit(hit.path);
    } else if (hit.preview) {
      this.viewing.set(this.previews().indexOf(hit));
    } else {
      this.openFolder.emit(this.folder(hit));
    }
  }
}
