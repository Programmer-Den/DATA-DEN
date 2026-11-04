import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { Component, OnInit, computed, inject, input, signal } from '@angular/core';
import { errorMessage } from '../auth.service';
import { Entry } from '../files/files';
import { formatSize } from '../format';

/** What a link shares, as /api/s/{id} tells it. */
interface Shared {
  name: string;
  dir: boolean;
  size?: number;
  expires: string;
}

interface Listing {
  path: string;
  entries: Entry[];
}

interface Crumb {
  name: string;
  path: string;
}

const date = new Intl.DateTimeFormat('de-DE', { dateStyle: 'medium', timeStyle: 'short' });

/** The page behind a share link: no login, maybe a password, then download. */
@Component({
  selector: 'app-public-share',
  templateUrl: './public-share.html',
  styleUrl: './public-share.css',
})
export class PublicShare implements OnInit {
  private readonly http = inject(HttpClient);

  /** The id from the link /s/<id>. */
  readonly id = input.required<string>();

  protected readonly shared = signal<Shared | null>(null);
  /** True while the link waits for its password. */
  protected readonly locked = signal(false);
  protected readonly error = signal<string | null>(null);
  protected readonly passwordError = signal<string | null>(null);
  protected readonly busy = signal(false);
  /** The folder shown within a shared folder, "/" for the shared folder itself. */
  protected readonly path = signal('/');
  protected readonly entries = signal<Entry[] | null>(null);
  protected readonly crumbs = computed<Crumb[]>(() => {
    const parts = this.path().split('/').filter(Boolean);
    return [
      { name: this.shared()?.name ?? '', path: '/' },
      ...parts.map((name, i) => ({ name, path: '/' + parts.slice(0, i + 1).join('/') })),
    ];
  });
  protected readonly formatSize = formatSize;

  private get base(): string {
    return '/api/s/' + this.id();
  }

  ngOnInit(): void {
    this.http.get<Shared>(this.base).subscribe({
      next: (shared) => this.show(shared),
      error: (err: unknown) => {
        if (err instanceof HttpErrorResponse && err.status === 401 && err.error?.password) {
          this.locked.set(true);
        } else {
          this.error.set(errorMessage(err));
        }
      },
    });
  }

  protected unlock(event: Event, password: string): void {
    event.preventDefault();
    this.busy.set(true);
    this.passwordError.set(null);
    this.http.post<Shared>(this.base + '/unlock', { password }).subscribe({
      next: (shared) => {
        this.busy.set(false);
        this.locked.set(false);
        this.show(shared);
      },
      error: (err: unknown) => {
        this.busy.set(false);
        this.passwordError.set(errorMessage(err));
      },
    });
  }

  private show(shared: Shared): void {
    this.shared.set(shared);
    if (shared.dir) {
      this.open('/');
    }
  }

  protected open(path: string): void {
    this.error.set(null);
    this.http.get<Listing>(this.base + '/files', { params: { path } }).subscribe({
      next: (listing) => {
        this.path.set(listing.path);
        this.entries.set(listing.entries);
      },
      error: (err: unknown) => this.error.set(errorMessage(err)),
    });
  }

  protected child(entry: Entry): string {
    return (this.path() === '/' ? '/' : this.path() + '/') + entry.name;
  }

  /** The file to save; no path for a shared file. */
  protected downloadUrl(path?: string): string {
    return this.base + '/download' + (path ? '?path=' + encodeURIComponent(path) : '');
  }

  /** The open folder as one ZIP. */
  protected zipUrl(): string {
    return this.base + '/zip?path=' + encodeURIComponent(this.path());
  }

  protected date(when: string): string {
    return date.format(new Date(when));
  }
}
