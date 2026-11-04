import { HttpClient } from '@angular/common/http';
import { Component, OnInit, inject, input, output, signal } from '@angular/core';
import { errorMessage } from '../auth.service';
import { Grant } from './grant';

/** Shares a folder with another account, to read or also to change it. */
@Component({
  selector: 'app-grant-form',
  templateUrl: './grant-form.html',
  styleUrl: './shares.css',
})
export class GrantForm implements OnInit {
  private readonly http = inject(HttpClient);

  /** The folder to share, such as "/Projekt". */
  readonly path = input.required<string>();
  readonly name = input.required<string>();
  readonly closed = output<void>();

  /** The accounts it can be shared with. */
  protected readonly accounts = signal<string[] | null>(null);
  protected readonly busy = signal(false);
  protected readonly error = signal<string | null>(null);
  /** The grant, once made. */
  protected readonly done = signal<Grant | null>(null);

  ngOnInit(): void {
    this.http.get<{ names: string[] }>('/api/accounts').subscribe({
      next: (answer) => this.accounts.set(answer.names),
      error: (err: unknown) => this.error.set(errorMessage(err)),
    });
  }

  protected create(event: Event, to: string, write: boolean): void {
    event.preventDefault();
    this.busy.set(true);
    this.error.set(null);
    this.http.post<Grant>('/api/grants', { path: this.path(), to, write }).subscribe({
      next: (grant) => {
        this.busy.set(false);
        this.done.set(grant);
      },
      error: (err: unknown) => {
        this.busy.set(false);
        this.error.set(errorMessage(err));
      },
    });
  }
}
