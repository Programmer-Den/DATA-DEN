import { HttpClient } from '@angular/common/http';
import {
  Component,
  ElementRef,
  OnInit,
  afterRenderEffect,
  computed,
  inject,
  input,
  output,
  signal,
  viewChild,
} from '@angular/core';
import { errorMessage } from '../auth.service';

interface Opened {
  url: string;
  token: string;
  ttl: number;
}

/**
 * Edits an office document with Collabora over the page. Collabora saves by
 * itself; the first save keeps the file as it was as a version.
 */
@Component({
  selector: 'app-office-frame',
  templateUrl: './office-frame.html',
  styleUrl: './office-frame.css',
  host: { '(window:message)': 'message($event)' },
})
export class OfficeFrame implements OnInit {
  private readonly http = inject(HttpClient);

  /** The document, such as "/Briefe/antrag.docx". */
  readonly path = input.required<string>();
  readonly closed = output<void>();

  protected readonly name = computed(() => this.path().split('/').pop() ?? '');
  protected readonly opened = signal<Opened | null>(null);
  protected readonly error = signal<string | null>(null);
  private readonly form = viewChild<ElementRef<HTMLFormElement>>('form');
  private submitted = false;

  constructor() {
    // The token goes to Collabora in a POST, never in the address.
    afterRenderEffect(() => {
      const form = this.form();
      if (form && !this.submitted) {
        this.submitted = true;
        form.nativeElement.submit();
      }
    });
  }

  ngOnInit(): void {
    this.http.post<Opened>('/api/office', { path: this.path() }).subscribe({
      next: (opened) => this.opened.set(opened),
      error: (err: unknown) => this.error.set(errorMessage(err)),
    });
  }

  /** Collabora's "Schließen" in its own menu. */
  protected message(event: MessageEvent): void {
    if (event.origin !== location.origin || typeof event.data !== 'string') {
      return;
    }
    try {
      const data = JSON.parse(event.data) as { MessageId?: string };
      if (data.MessageId === 'UI_Close') {
        this.closed.emit();
      }
    } catch {
      // Not one of Collabora's messages.
    }
  }
}
