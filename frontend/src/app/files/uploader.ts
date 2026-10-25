import { HttpClient, HttpEventType, HttpResponse } from '@angular/common/http';
import { Injectable, inject, signal } from '@angular/core';
import { filter, firstValueFrom, lastValueFrom, tap } from 'rxjs';
import { errorMessage } from '../auth.service';

/** A file to upload and where it goes, such as "/Fotos/2026/a.jpg". */
export interface Job {
  file: File;
  path: string;
}

export type UploadStatus = 'wartet' | 'lädt hoch' | 'fertig' | 'Fehler';

/** One upload as the page shows it. */
export interface UploadItem {
  id: number;
  path: string;
  size: number;
  loaded: number;
  status: UploadStatus;
  error?: string;
}

/** Uploads at the same time. */
export const PARALLEL = 3;
/** Bytes per request; a broken request only repeats this much. */
export const CHUNK_SIZE = 8 << 20;
/** Attempts per chunk before the file counts as failed. */
const ATTEMPTS = 3;

/**
 * Uploads files in chunks, a few at a time, and keeps the progress of each.
 * A chunk that fails is tried again from where the server says it stands.
 */
@Injectable({ providedIn: 'root' })
export class Uploader {
  private readonly http = inject(HttpClient);
  private nextId = 1;

  readonly items = signal<UploadItem[]>([]);

  /** Uploads all jobs; resolves when every one has finished or failed. */
  async upload(jobs: Job[]): Promise<void> {
    const queue = jobs.map((job) => ({ job, id: this.add(job) }));
    const worker = async () => {
      for (let next = queue.shift(); next; next = queue.shift()) {
        await this.uploadOne(next.id, next.job);
      }
    };
    await Promise.all(Array.from({ length: Math.min(PARALLEL, queue.length) }, worker));
  }

  /** Removes the finished uploads from the list. */
  clearDone(): void {
    this.items.update((items) => items.filter((item) => item.status !== 'fertig'));
  }

  private add(job: Job): number {
    const id = this.nextId++;
    this.items.update((items) => [
      ...items,
      { id, path: job.path, size: job.file.size, loaded: 0, status: 'wartet' },
    ]);
    return id;
  }

  private set(id: number, change: Partial<UploadItem>): void {
    this.items.update((items) =>
      items.map((item) => (item.id === id ? { ...item, ...change } : item)),
    );
  }

  private async uploadOne(id: number, job: Job): Promise<void> {
    this.set(id, { status: 'lädt hoch' });
    try {
      const started = await firstValueFrom(
        this.http.post<{ id: string; offset: number }>('/api/uploads', {
          path: job.path,
          size: job.file.size,
        }),
      );
      let offset = started.offset;
      let failures = 0;
      while (offset < job.file.size) {
        try {
          offset = await this.sendChunk(id, started.id, job.file, offset);
          failures = 0;
        } catch (err) {
          if (++failures >= ATTEMPTS) {
            throw err;
          }
          // Asks where the server stands and goes on from there.
          const state = await firstValueFrom(
            this.http.get<{ offset: number }>(`/api/uploads/${started.id}`),
          );
          offset = state.offset;
        }
      }
      this.set(id, { loaded: job.file.size, status: 'fertig' });
    } catch (err) {
      this.set(id, { status: 'Fehler', error: errorMessage(err) });
    }
  }

  /** Sends the chunk at offset and returns the server's new offset. */
  private async sendChunk(id: number, uploadId: string, file: File, offset: number) {
    const chunk = file.slice(offset, offset + CHUNK_SIZE);
    const response = await lastValueFrom(
      this.http
        .put<{ offset: number }>(`/api/uploads/${uploadId}`, chunk, {
          headers: { 'Upload-Offset': String(offset), 'Content-Type': 'application/octet-stream' },
          observe: 'events',
          reportProgress: true,
        })
        .pipe(
          tap((event) => {
            if (event.type === HttpEventType.UploadProgress) {
              this.set(id, { loaded: offset + event.loaded });
            }
          }),
          filter((event) => event instanceof HttpResponse),
        ),
    );
    const next = response.body!.offset;
    this.set(id, { loaded: next });
    return next;
  }
}

/** The files and folders dropped onto the page, with their paths below target. */
export async function droppedJobs(data: DataTransfer, target: string): Promise<Job[]> {
  const base = target === '/' ? '' : target;
  const entries = Array.from(data.items)
    .map((item) => item.webkitGetAsEntry?.())
    .filter((entry): entry is FileSystemEntry => !!entry);
  if (entries.length === 0) {
    return Array.from(data.files).map((file) => ({ file, path: `${base}/${file.name}` }));
  }
  const jobs: Job[] = [];
  for (const entry of entries) {
    await collect(entry, base, jobs);
  }
  return jobs;
}

async function collect(entry: FileSystemEntry, base: string, jobs: Job[]): Promise<void> {
  const path = `${base}/${entry.name}`;
  if (entry.isFile) {
    const file = await new Promise<File>((resolve, reject) =>
      (entry as FileSystemFileEntry).file(resolve, reject),
    );
    jobs.push({ file, path });
    return;
  }
  const reader = (entry as FileSystemDirectoryEntry).createReader();
  // readEntries returns the folder in batches, then an empty one.
  for (;;) {
    const batch = await new Promise<FileSystemEntry[]>((resolve, reject) =>
      reader.readEntries(resolve, reject),
    );
    if (batch.length === 0) {
      return;
    }
    for (const child of batch) {
      await collect(child, path, jobs);
    }
  }
}

/** The files picked in a file input, with their paths below target. */
export function pickedJobs(files: FileList, target: string): Job[] {
  const base = target === '/' ? '' : target;
  // A folder pick fills webkitRelativePath, such as "Urlaub/a.jpg".
  return Array.from(files).map((file) => ({
    file,
    path: `${base}/${file.webkitRelativePath || file.name}`,
  }));
}
