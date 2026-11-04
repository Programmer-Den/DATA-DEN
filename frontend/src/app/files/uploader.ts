import { HttpClient, HttpErrorResponse, HttpEventType, HttpResponse } from '@angular/common/http';
import { Injectable, inject, signal } from '@angular/core';
import { filter, firstValueFrom, lastValueFrom, tap } from 'rxjs';
import { errorMessage } from '../auth.service';

/** A file to upload and where it goes, such as "/Fotos/2026/a.jpg". */
export interface Job {
  file: File;
  path: string;
}

export type UploadStatus = 'wartet' | 'lädt hoch' | 'fertig' | 'Fehler' | 'existiert schon';

/** One upload as the page shows it. */
export interface UploadItem {
  id: number;
  path: string;
  size: number;
  loaded: number;
  status: UploadStatus;
  error?: string;
  /** Files that already had the same content, such as "/Fotos/a.jpg". */
  duplicates?: string[];
  /** True when the upload replaced a file, whose old content is a version now. */
  replaced?: boolean;
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
  /** The files that exist already, by item id, until replaced or skipped. */
  private readonly existing = new Map<number, Job>();

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

  /**
   * Uploads the files that existed already again, replacing them; the server
   * keeps their old content as a version. Without ids, all of them.
   */
  async replace(ids = [...this.existing.keys()]): Promise<void> {
    const queue = ids.flatMap((id) => {
      const job = this.existing.get(id);
      this.existing.delete(id);
      if (job) {
        this.set(id, { status: 'wartet', error: undefined, loaded: 0 });
      }
      return job ? [{ job, id }] : [];
    });
    const worker = async () => {
      for (let next = queue.shift(); next; next = queue.shift()) {
        await this.uploadOne(next.id, next.job, true);
      }
    };
    await Promise.all(Array.from({ length: Math.min(PARALLEL, queue.length) }, worker));
  }

  /** Leaves a file that existed already as it is and drops it from the list. */
  skip(id: number): void {
    this.existing.delete(id);
    this.items.update((items) => items.filter((item) => item.id !== id));
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

  private async uploadOne(id: number, job: Job, replace = false): Promise<void> {
    this.set(id, { status: 'lädt hoch' });
    let starting = true;
    try {
      const started = await firstValueFrom(
        this.http.post<{ id: string; offset: number }>('/api/uploads', {
          path: job.path,
          size: job.file.size,
          ...(replace ? { replace } : {}),
        }),
      );
      starting = false;
      let offset = started.offset;
      let failures = 0;
      let duplicates: string[] | undefined;
      while (offset < job.file.size) {
        try {
          const answer = await this.sendChunk(id, started.id, job.file, offset);
          offset = answer.offset;
          duplicates = answer.duplicates;
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
      this.set(id, {
        loaded: job.file.size,
        status: 'fertig',
        ...(duplicates?.length ? { duplicates } : {}),
        ...(replace ? { replaced: true } : {}),
      });
    } catch (err) {
      if (starting && !replace && err instanceof HttpErrorResponse && err.status === 409) {
        // The file exists already: the page asks whether to replace it.
        this.existing.set(id, job);
        this.set(id, { status: 'existiert schon' });
        return;
      }
      this.set(id, { status: 'Fehler', error: errorMessage(err) });
    }
  }

  /**
   * Sends the chunk at offset and returns the server's answer: its new
   * offset and, after the last chunk, the files with the same content.
   */
  private async sendChunk(id: number, uploadId: string, file: File, offset: number) {
    const chunk = file.slice(offset, offset + CHUNK_SIZE);
    const response = await lastValueFrom(
      this.http
        .put<{ offset: number; duplicates?: string[] }>(`/api/uploads/${uploadId}`, chunk, {
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
    const answer = response.body!;
    this.set(id, { loaded: answer.offset });
    return answer;
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
