import { HttpClient } from '@angular/common/http';
import { Injectable, inject, signal } from '@angular/core';

/**
 * Which office documents can be edited, as the server says; none when it
 * runs without Collabora. The app loads it once after the login.
 */
@Injectable({ providedIn: 'root' })
export class OfficeFormats {
  private readonly http = inject(HttpClient);
  private readonly extensions = signal<string[]>([]);
  private asked = false;

  load(): void {
    if (this.asked) {
      return;
    }
    this.asked = true;
    this.http.get<{ extensions: string[] }>('/api/office').subscribe({
      next: (answer) => this.extensions.set(answer.extensions),
      // Without Collabora the server has no /api/office; ask again on the
      // next login.
      error: () => (this.asked = false),
    });
  }

  /** True when the file can be opened in the office editor. */
  canEdit(name: string): boolean {
    const lower = name.toLowerCase();
    return this.extensions().some((ext) => lower.endsWith(ext));
  }
}
