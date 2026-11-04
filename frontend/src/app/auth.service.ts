import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { Injectable, inject, signal } from '@angular/core';
import { Observable, map, tap } from 'rxjs';

export interface Session {
  username: string;
  /** Admins manage the accounts. */
  admin?: boolean;
  /** The owner's account comes from the server's settings. */
  owner?: boolean;
}

/** The login: who is logged in, and logging in and out. */
@Injectable({ providedIn: 'root' })
export class AuthService {
  private readonly http = inject(HttpClient);

  /** The logged-in user, null when nobody is, undefined until the server answered. */
  readonly user = signal<string | null | undefined>(undefined);
  /** Whether the logged-in user manages the accounts. */
  readonly admin = signal(false);
  /** Whether the logged-in user is the owner, whose password is in the server's settings. */
  readonly owner = signal(false);

  /** Asks the server whether the session cookie is still valid. */
  check(): void {
    this.http.get<Session>('/api/session').subscribe({
      next: (session) => this.set(session),
      error: () => this.set(null),
    });
  }

  login(username: string, password: string): Observable<void> {
    return this.http.post<Session>('/api/login', { username, password }).pipe(
      tap((session) => this.set(session)),
      map(() => undefined),
    );
  }

  logout(): Observable<void> {
    return this.http.post<void>('/api/logout', null).pipe(tap(() => this.set(null)));
  }

  private set(session: Session | null): void {
    this.admin.set(session?.admin ?? false);
    this.owner.set(session?.owner ?? false);
    this.user.set(session?.username ?? null);
  }
}

/** The server's message for a failed request, in German like the UI. */
export function errorMessage(err: unknown): string {
  if (err instanceof HttpErrorResponse && typeof err.error?.error === 'string') {
    return err.error.error;
  }
  return 'Server nicht erreichbar';
}
