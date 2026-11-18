import {
  CHUNK,
  Damaged,
  WrongSecret,
  changePassphrase,
  createVault,
  formatRecovery,
  newRecovery,
  openFile,
  openName,
  parseRecovery,
  plainSize,
  sealFile,
  sealName,
  unlock,
} from './vault-crypto';

// Few iterations keep the tests fast; the real ones use PBKDF2_ITERATIONS.
const FAST = 100_000;

async function bytesOf(blob: Blob): Promise<Uint8Array<ArrayBuffer>> {
  return new Uint8Array(await blob.arrayBuffer());
}

function same(a: Uint8Array, b: Uint8Array): boolean {
  return a.length === b.length && a.every((v, i) => v === b[i]);
}

describe('vault crypto', () => {
  it('formats and parses the recovery key, forgiving how it is typed', () => {
    const bytes = crypto.getRandomValues(new Uint8Array(32));
    const text = formatRecovery(bytes);
    expect(text).toMatch(/^([0-9A-Z]{4}-){12}[0-9A-Z]{4}$/);
    expect(parseRecovery(text)).toEqual(bytes);
    expect(parseRecovery(text.toLowerCase().replace(/-/g, ' '))).toEqual(bytes);
    expect(parseRecovery(text.slice(0, -1))).toBeNull();
    expect(parseRecovery(text.slice(0, -1) + 'U')).toBeNull();
  });

  it('opens with the passphrase and with the recovery key, not with wrong ones', async () => {
    const made = await createVault('richtig lange passphrase', FAST);
    expect(JSON.stringify(made.keys)).not.toContain('richtig');
    const name = await sealName(made.vault, 'Steuer 2026.pdf');

    const byPassphrase = await unlock(made.keys, { passphrase: 'richtig lange passphrase' });
    expect(await openName(byPassphrase, name)).toBe('Steuer 2026.pdf');
    const byRecovery = await unlock(made.keys, { recovery: made.recovery });
    expect(await openName(byRecovery, name)).toBe('Steuer 2026.pdf');

    await expect(unlock(made.keys, { passphrase: 'falsch' })).rejects.toBeInstanceOf(WrongSecret);
    await expect(
      unlock(made.keys, { recovery: 'nicht einmal ein Schlüssel' }),
    ).rejects.toBeInstanceOf(WrongSecret);
    const other = await createVault('andere passphrase', FAST);
    await expect(unlock(made.keys, { recovery: other.recovery })).rejects.toBeInstanceOf(
      WrongSecret,
    );
    expect(await openName(other.vault, name)).toBeNull();
  });

  it('keeps the vault key and proof when the passphrase or recovery key changes', async () => {
    const made = await createVault('alte passphrase', FAST);
    const name = await sealName(made.vault, 'a.txt');

    const changed = await changePassphrase(
      made.keys,
      { recovery: made.recovery },
      'neue passphrase',
      FAST,
    );
    expect(changed.proof).toBe(made.proof);
    expect(changed.keys.recovery).toEqual(made.keys.recovery);
    await expect(unlock(changed.keys, { passphrase: 'alte passphrase' })).rejects.toBeInstanceOf(
      WrongSecret,
    );
    expect(
      await openName(await unlock(changed.keys, { passphrase: 'neue passphrase' }), name),
    ).toBe('a.txt');

    const renewed = await newRecovery(changed.keys, { passphrase: 'neue passphrase' });
    expect(renewed.proof).toBe(made.proof);
    expect(renewed.recovery).not.toBe(made.recovery);
    await expect(unlock(renewed.keys, { recovery: made.recovery })).rejects.toBeInstanceOf(
      WrongSecret,
    );
    expect(await openName(await unlock(renewed.keys, { recovery: renewed.recovery }), name)).toBe(
      'a.txt',
    );
  });

  it('seals names so that the server cannot read or compare them', async () => {
    const { vault } = await createVault('passphrase lang genug', FAST);
    const a = await sealName(vault, 'Arztbrief.pdf');
    const b = await sealName(vault, 'Arztbrief.pdf');
    expect(a).not.toBe(b);
    expect(a).toMatch(/^[A-Za-z0-9_-]{39,255}$/);
    expect(await sealName(vault, 'ü'.repeat(80))).toHaveLength(251);
    await expect(sealName(vault, 'x'.repeat(161))).rejects.toThrow();
    await expect(sealName(vault, '')).rejects.toThrow();
    expect(await openName(vault, a.slice(0, -2) + (a.endsWith('AA') ? 'BB' : 'AA'))).toBeNull();
    expect(await openName(vault, 'kein base64!')).toBeNull();
  });

  it('seals files of every size, in chunks', async () => {
    const { vault } = await createVault('passphrase lang genug', FAST);
    for (const size of [0, 1, 1000, CHUNK, CHUNK + 1, 2 * CHUNK + 5]) {
      const plain = new Uint8Array(size);
      for (let i = 0; i < size; i++) {
        plain[i] = i % 251;
      }
      const sealed = await sealFile(vault, new Blob([plain]));
      const chunks = Math.max(1, Math.ceil(size / CHUNK));
      expect(sealed.size).toBe(4 + 32 + size + 16 * chunks);
      expect(plainSize(sealed.size)).toBe(size);
      expect(new TextDecoder().decode((await bytesOf(sealed)).subarray(0, 4))).toBe('DDV1');
      const opened = await openFile(vault, sealed, 'application/pdf');
      expect(opened.type).toBe('application/pdf');
      // toEqual takes seconds on megabytes.
      expect(same(await bytesOf(opened), plain)).toBe(true);
    }
  });

  it('refuses files that were changed, cut off or come from another vault', async () => {
    const { vault } = await createVault('passphrase lang genug', FAST);
    const plain = new Uint8Array(CHUNK + 100).fill(42);
    const sealed = await bytesOf(await sealFile(vault, new Blob([plain])));

    const flipped = sealed.slice();
    flipped[100] ^= 1;
    await expect(openFile(vault, new Blob([flipped]))).rejects.toBeInstanceOf(Damaged);
    // Cut after the first chunk: that one was not sealed as the last.
    const cut = sealed.slice(0, 4 + 32 + CHUNK + 16);
    await expect(openFile(vault, new Blob([cut]))).rejects.toBeInstanceOf(Damaged);
    await expect(openFile(vault, new Blob([sealed.slice(0, 40)]))).rejects.toBeInstanceOf(Damaged);
    const other = await createVault('passphrase lang genug', FAST);
    await expect(openFile(other.vault, new Blob([sealed]))).rejects.toBeInstanceOf(Damaged);
  });
});
