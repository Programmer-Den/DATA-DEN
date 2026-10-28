import { HttpClient } from '@angular/common/http';
import { Component, computed, effect, inject, input, model, output, signal } from '@angular/core';
import { DomSanitizer } from '@angular/platform-browser';
import { errorMessage } from '../auth.service';
import { Item, downloadUrl, isPdf, viewUrl } from './item';

/** Text files up to this size are shown; larger ones are only downloaded. */
export const MAX_TEXT = 1 << 20;

/**
 * Shows one file of a list over the page: a photo, a video or song with its
 * player, a PDF or a text file. The arrow keys go through the list, Escape
 * closes it.
 */
@Component({
  selector: 'app-viewer',
  templateUrl: './viewer.html',
  styleUrl: './viewer.css',
  host: { '(document:keydown)': 'key($event)' },
})
export class Viewer {
  private readonly http = inject(HttpClient);
  private readonly sanitizer = inject(DomSanitizer);

  readonly items = input.required<Item[]>();
  /** The file shown, an index into items. */
  readonly index = model.required<number>();
  readonly closed = output<void>();

  protected readonly item = computed<Item | undefined>(() => this.items()[this.index()]);
  protected readonly url = computed(() => viewUrl(this.item()?.path ?? ''));
  protected readonly download = computed(() => downloadUrl(this.item()?.path ?? ''));
  /** What to show the file with. */
  protected readonly shape = computed(() => {
    const item = this.item();
    if (!item?.preview) {
      return 'none';
    }
    if (item.kind === 'document') {
      if (isPdf(item)) {
        return 'pdf';
      }
      return item.size <= MAX_TEXT ? 'text' : 'none';
    }
    return item.kind ?? 'none';
  });
  /** The PDF's address for the frame; Angular only lets trusted ones in. */
  protected readonly frameUrl = computed(() =>
    this.sanitizer.bypassSecurityTrustResourceUrl(this.url()),
  );
  protected readonly text = signal<string | null>(null);
  /** Why the file cannot be shown, or null. */
  protected readonly failed = signal<string | null>(null);

  constructor() {
    effect((onCleanup) => {
      this.failed.set(null);
      this.text.set(null);
      if (this.shape() !== 'text') {
        return;
      }
      const sub = this.http.get(this.url(), { responseType: 'text' }).subscribe({
        next: (text) => this.text.set(text),
        error: (err: unknown) => this.failed.set(errorMessage(err)),
      });
      onCleanup(() => sub.unsubscribe());
    });
  }

  protected step(by: number): void {
    const next = this.index() + by;
    if (next >= 0 && next < this.items().length) {
      this.index.set(next);
    }
  }

  protected key(event: KeyboardEvent): void {
    if (event.key === 'Escape') {
      this.closed.emit();
    } else if (event.key === 'ArrowLeft') {
      this.step(-1);
    } else if (event.key === 'ArrowRight') {
      this.step(1);
    }
  }

  /** A photo or video the browser cannot decode, such as HEIC in Chrome. */
  protected mediaError(): void {
    this.failed.set('Dieser Browser kann die Datei nicht anzeigen.');
  }
}
