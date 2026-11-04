import { HttpClient } from '@angular/common/http';
import { Component, inject, signal } from '@angular/core';
import { Observable } from 'rxjs';
import { errorMessage } from '../auth.service';
import { Grant, rights } from '../shares/grant';
import { User } from '../users/users';

/** A team folder with its members, as /api/teams lists it. */
export interface Team {
  name: string;
  members: Grant[];
}

/**
 * The team folders, for admins: folders no account owns, for several
 * accounts, each to read or also to change.
 */
@Component({
  selector: 'app-teams',
  templateUrl: './teams.html',
  styleUrl: './teams.css',
})
export class TeamsView {
  private readonly http = inject(HttpClient);

  protected readonly teams = signal<Team[] | null>(null);
  /** All accounts, to add as members. */
  protected readonly accounts = signal<string[]>([]);
  protected readonly error = signal<string | null>(null);
  protected readonly done = signal<string | null>(null);
  protected readonly busy = signal(false);
  protected readonly rights = rights;

  constructor() {
    this.load();
    this.http.get<{ users: User[] }>('/api/users').subscribe({
      next: (answer) => this.accounts.set(answer.users.map((user) => user.name)),
      error: (err: unknown) => this.error.set(errorMessage(err)),
    });
  }

  private load(): void {
    this.http.get<{ teams: Team[] }>('/api/teams').subscribe({
      next: (answer) => this.teams.set(answer.teams),
      error: (err: unknown) => this.error.set(errorMessage(err)),
    });
  }

  protected create(event: Event, input: HTMLInputElement): void {
    event.preventDefault();
    const name = input.value.trim();
    if (!name) {
      return;
    }
    this.run(this.http.post('/api/teams', { name }), `Team-Ordner „${name}“ angelegt`, () => {
      input.value = '';
    });
  }

  protected setMember(event: Event, team: Team, to: string, write: boolean): void {
    event.preventDefault();
    this.run(
      this.http.post(`/api/teams/${team.name}/members`, { to, write }),
      `${to} in „${team.name}“: ${write ? 'Lesen und Schreiben' : 'Nur lesen'}`,
    );
  }

  protected removeMember(team: Team, member: Grant): void {
    this.run(
      this.http.post(`/api/teams/${team.name}/members/delete`, { to: member.to }),
      `${member.to} aus „${team.name}“ entfernt`,
    );
  }

  protected remove(team: Team): void {
    if (
      !confirm(
        `Den Team-Ordner „${team.name}“ löschen? Die Mitglieder sehen ihn dann nicht mehr; der Inhalt wird auf dem Server beiseitegelegt.`,
      )
    ) {
      return;
    }
    this.run(this.http.post(`/api/teams/${team.name}/delete`, {}), `„${team.name}“ gelöscht`);
  }

  private run(request: Observable<unknown>, message: string, after?: () => void): void {
    this.busy.set(true);
    this.error.set(null);
    this.done.set(null);
    request.subscribe({
      next: () => {
        this.busy.set(false);
        this.done.set(message);
        after?.();
        this.load();
      },
      error: (err: unknown) => {
        this.busy.set(false);
        this.error.set(errorMessage(err));
      },
    });
  }
}
