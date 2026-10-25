import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { Injectable, inject, signal } from '@angular/core';
import { Observable, map, tap } from 'rxjs';

interface Session {
  username: string;
}

/** The login: who is logged in, and logging in and out. */
@Injectable({ providedIn: 'root' })
export class AuthService {
  private readonly http = inject(HttpClient);

  /** The logged-in user, null when nobody is, undefined until the server answered. */
  readonly user = signal<string | null | undefined>(undefined);

  /** Asks the server whether the session cookie is still valid. */
  check(): void {
    this.http.get<Session>('/api/session').subscribe({
      next: (session) => this.user.set(session.username),
      error: () => this.user.set(null),
    });
  }

  login(username: string, password: string): Observable<void> {
    return this.http.post<Session>('/api/login', { username, password }).pipe(
      tap((session) => this.user.set(session.username)),
      map(() => undefined),
    );
  }

  logout(): Observable<void> {
    return this.http.post<void>('/api/logout', null).pipe(tap(() => this.user.set(null)));
  }
}

/** The server's message for a failed request, in German like the UI. */
export function errorMessage(err: unknown): string {
  if (err instanceof HttpErrorResponse && typeof err.error?.error === 'string') {
    return err.error.error;
  }
  return 'Server nicht erreichbar';
}
