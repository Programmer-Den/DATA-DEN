import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { Component, computed, inject, input, OnInit, output, signal } from '@angular/core';
import { errorMessage } from '../auth.service';
import { isMarkdown, markdownHtml } from './markdown';

interface Text {
  content: string;
  version: string;
}

/**
 * Edits a text or Markdown file over the page. Ctrl+S saves; a file changed
 * elsewhere meanwhile is not overwritten. The first save of each session
 * keeps the file as it was as a version.
 */
@Component({
  selector: 'app-editor',
  templateUrl: './editor.html',
  styleUrl: './editor.css',
  host: {
    '(document:keydown)': 'key($event)',
    '(window:beforeunload)': 'leaving($event)',
  },
})
export class Editor implements OnInit {
  private readonly http = inject(HttpClient);

  /** The file, such as "/Notizen/einkauf.md". */
  readonly path = input.required<string>();
  /** True when the file was just made and has nothing to keep. */
  readonly fresh = input(false);
  /** After each save. */
  readonly saved = output<void>();
  readonly closed = output<void>();

  protected readonly name = computed(() => this.path().split('/').pop() ?? '');
  protected readonly markdown = computed(() => isMarkdown(this.name()));
  /** The text in the editor; null while it loads. */
  protected readonly content = signal<string | null>(null);
  /** What was saved last, to tell unsaved changes. */
  private readonly savedContent = signal('');
  protected readonly dirty = computed(() => {
    const content = this.content();
    return content !== null && content !== this.savedContent();
  });
  /** Markdown: the text, the preview or both side by side. */
  protected readonly mode = signal<'edit' | 'split' | 'preview'>('split');
  protected readonly preview = computed(() => markdownHtml(this.content() ?? ''));
  protected readonly saving = signal(false);
  protected readonly error = signal<string | null>(null);
  /** True when the file changed elsewhere since it was opened. */
  protected readonly conflict = signal(false);
  private version = '';
  private kept = false;

  ngOnInit(): void {
    this.kept = this.fresh();
    this.load();
  }

  private load(): void {
    this.content.set(null);
    this.error.set(null);
    this.conflict.set(false);
    this.http.get<Text>('/api/text', { params: { path: this.path() } }).subscribe({
      next: (text) => {
        this.version = text.version;
        this.savedContent.set(text.content);
        this.content.set(text.content);
      },
      error: (err: unknown) => this.error.set(errorMessage(err)),
    });
  }

  protected input(event: Event): void {
    this.content.set((event.target as HTMLTextAreaElement).value);
  }

  protected save(): void {
    const content = this.content();
    if (content === null || this.saving()) {
      return;
    }
    this.saving.set(true);
    this.error.set(null);
    const body = { path: this.path(), content, version: this.version, backup: !this.kept };
    this.http.put<{ version: string }>('/api/text', body).subscribe({
      next: (answer) => {
        this.saving.set(false);
        this.version = answer.version;
        this.kept = true;
        this.savedContent.set(content);
        this.saved.emit();
      },
      error: (err: unknown) => {
        this.saving.set(false);
        this.conflict.set(err instanceof HttpErrorResponse && err.status === 409);
        this.error.set(errorMessage(err));
      },
    });
  }

  /** Drops the changes and loads the file as it is now. */
  protected reload(): void {
    if (!this.dirty() || confirm('Deine Änderungen verwerfen und neu laden?')) {
      this.load();
    }
  }

  protected close(): void {
    if (!this.dirty() || confirm('Ungespeicherte Änderungen verwerfen?')) {
      this.closed.emit();
    }
  }

  protected key(event: KeyboardEvent): void {
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 's') {
      event.preventDefault();
      this.save();
    } else if (event.key === 'Escape') {
      this.close();
    }
  }

  protected leaving(event: BeforeUnloadEvent): void {
    if (this.dirty()) {
      event.preventDefault();
    }
  }
}
