import { HttpClient } from '@angular/common/http';
import { Component, inject, input, output, signal } from '@angular/core';
import { errorMessage } from '../auth.service';
import { Share, copy, shareDays, shareLink } from './share';

/** Makes a link to a file or folder: how long it works and an optional password. */
@Component({
  selector: 'app-share-form',
  templateUrl: './share-form.html',
  styleUrl: './shares.css',
})
export class ShareForm {
  private readonly http = inject(HttpClient);

  /** What to share, such as "/Fotos/2026". */
  readonly path = input.required<string>();
  readonly name = input.required<string>();
  /** True for a folder, which can also take an upload link. */
  readonly dir = input(false);
  readonly closed = output<void>();

  protected readonly shareDays = shareDays;
  protected readonly busy = signal(false);
  protected readonly error = signal<string | null>(null);
  /** The new link, once made. */
  protected readonly link = signal<string | null>(null);
  protected readonly copied = signal(false);
  /** Whether the new link is an upload link, as chosen and then as made. */
  protected readonly upload = signal(false);

  protected create(event: Event, days: string, password: string): void {
    const upload = this.dir() && this.upload();
    event.preventDefault();
    this.busy.set(true);
    this.error.set(null);
    this.http
      .post<Share>('/api/shares', { path: this.path(), days: Number(days), password, upload })
      .subscribe({
        next: (share) => {
          this.busy.set(false);
          this.upload.set(!!share.upload);
          this.link.set(shareLink(share.id));
        },
        error: (err: unknown) => {
          this.busy.set(false);
          this.error.set(errorMessage(err));
        },
      });
  }

  protected async copy(link: string): Promise<void> {
    this.copied.set(await copy(link));
  }
}
