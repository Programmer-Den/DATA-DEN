import { Injectable } from '@angular/core';

/** The folder the folder view opens next, such as one picked in the search. */
@Injectable({ providedIn: 'root' })
export class StartFolder {
  private next: string | null = null;

  set(path: string): void {
    this.next = path;
  }

  /** The folder to open, "/" when none was set; it is used only once. */
  take(): string {
    const path = this.next ?? '/';
    this.next = null;
    return path;
  }
}
