/**
 * The encryption of the vault (Tresor), all in the browser with WebCrypto:
 * the server only ever sees ciphertext, encrypted names and wrapped keys.
 *
 * - The vault key is 32 random bytes. It is stored twice, wrapped with
 *   AES-256-GCM: once under a key from the passphrase (PBKDF2-SHA256,
 *   600 000 iterations) and once under a key from the recovery key
 *   (32 random bytes, HKDF-SHA256).
 * - Names are sealed with AES-256-GCM under a key derived from the vault
 *   key (HKDF-SHA256), each with a random 12-byte nonce:
 *   base64url(nonce || ciphertext || tag).
 * - Files use the STREAM construction (Hoang, Reyhanitabar, Rogaway, Vizár
 *   2015), as in age and Tink's AES-GCM-HKDF streaming AEAD: a fresh key per
 *   file from HKDF-SHA256 with a random 32-byte salt, then chunks of 1 MiB
 *   sealed with AES-256-GCM under the nonce counter (11 bytes, big endian)
 *   || last flag (1 byte). Chunks cannot be reordered, dropped or cut off
 *   without the decryption failing. Layout: "DDV1" || salt || chunks.
 * - The proof sent with new wrapped keys is HKDF-SHA256 of the vault key, so
 *   the server can check that the browser holds the key without learning it.
 */

export const PBKDF2_ITERATIONS = 600_000;
export const CHUNK = 1 << 20;
const MAGIC = 'DDV1';
const SALT = 32;
const TAG = 16;
const NONCE = 12;
/** The longest name, in UTF-8 bytes, so the sealed name fits in 255 characters. */
export const MAX_NAME_BYTES = 160;

export interface PassphraseKey {
  kdf: 'PBKDF2-SHA256';
  iterations: number;
  salt: string;
  iv: string;
  wrapped: string;
}

export interface RecoveryKey {
  kdf: 'HKDF-SHA256';
  salt: string;
  iv: string;
  wrapped: string;
}

/** What the server keeps: the vault key, wrapped twice. */
export interface VaultKeys {
  version: 1;
  passphrase: PassphraseKey;
  recovery: RecoveryKey;
}

/** An unlocked vault: keys that cannot be read back out of the browser. */
export interface Vault {
  /** The vault key, only for deriving the others. */
  master: CryptoKey;
  names: CryptoKey;
}

/** How to open the wrapped vault key. */
export type Secret = { passphrase: string } | { recovery: string };

const subtle = (): SubtleCrypto => globalThis.crypto.subtle;
const enc = new TextEncoder();
const dec = new TextDecoder('utf-8', { fatal: true });

function random(n: number): Uint8Array<ArrayBuffer> {
  return globalThis.crypto.getRandomValues(new Uint8Array(n));
}

export function toBase64(bytes: Uint8Array): string {
  let s = '';
  for (const b of bytes) {
    s += String.fromCharCode(b);
  }
  return btoa(s);
}

export function fromBase64(s: string): Uint8Array<ArrayBuffer> {
  const bin = atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) {
    out[i] = bin.charCodeAt(i);
  }
  return out;
}

function toBase64Url(bytes: Uint8Array): string {
  return toBase64(bytes).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

function fromBase64Url(s: string): Uint8Array<ArrayBuffer> {
  if (!/^[A-Za-z0-9_-]*$/.test(s)) {
    throw new Error('not base64url');
  }
  const b64 = s.replace(/-/g, '+').replace(/_/g, '/');
  return fromBase64(b64 + '='.repeat((4 - (b64.length % 4)) % 4));
}

// Crockford's base32, for a recovery key that can be typed.
const BASE32 = '0123456789ABCDEFGHJKMNPQRSTVWXYZ';

/** The recovery key as 13 groups of 4 characters. */
export function formatRecovery(bytes: Uint8Array): string {
  let bits = 0;
  let value = 0;
  let out = '';
  for (const b of bytes) {
    value = (value << 8) | b;
    bits += 8;
    while (bits >= 5) {
      out += BASE32[(value >>> (bits - 5)) & 31];
      bits -= 5;
    }
  }
  if (bits > 0) {
    out += BASE32[(value << (5 - bits)) & 31];
  }
  return out.match(/.{1,4}/g)!.join('-');
}

/** The 32 bytes of a typed recovery key; null when it is not one. */
export function parseRecovery(text: string): Uint8Array<ArrayBuffer> | null {
  const chars = text.toUpperCase().replace(/[\s-]/g, '').replace(/[IL]/g, '1').replace(/O/g, '0');
  if (chars.length !== 52) {
    return null;
  }
  const out = new Uint8Array(32);
  let bits = 0;
  let value = 0;
  let i = 0;
  for (const c of chars) {
    const v = BASE32.indexOf(c);
    if (v < 0) {
      return null;
    }
    value = ((value << 5) | v) & 0xffff;
    bits += 5;
    if (bits >= 8) {
      if (i === 32) {
        return null;
      }
      out[i++] = (value >>> (bits - 8)) & 0xff;
      bits -= 8;
    }
  }
  // 52 characters hold 260 bits: the last 4 must be zero.
  if (i !== 32 || (value & ((1 << bits) - 1)) !== 0) {
    return null;
  }
  return out;
}

async function passphraseKek(
  passphrase: string,
  salt: Uint8Array<ArrayBuffer>,
  iterations: number,
) {
  const base = await subtle().importKey('raw', enc.encode(passphrase), 'PBKDF2', false, [
    'deriveKey',
  ]);
  return subtle().deriveKey(
    { name: 'PBKDF2', hash: 'SHA-256', salt, iterations },
    base,
    { name: 'AES-GCM', length: 256 },
    false,
    ['encrypt', 'decrypt'],
  );
}

async function recoveryKek(recovery: Uint8Array<ArrayBuffer>, salt: Uint8Array<ArrayBuffer>) {
  const base = await subtle().importKey('raw', recovery, 'HKDF', false, ['deriveKey']);
  return subtle().deriveKey(
    { name: 'HKDF', hash: 'SHA-256', salt, info: enc.encode('data-den vault recovery') },
    base,
    { name: 'AES-GCM', length: 256 },
    false,
    ['encrypt', 'decrypt'],
  );
}

async function wrap(kek: CryptoKey, raw: Uint8Array<ArrayBuffer>) {
  const iv = random(NONCE);
  const wrapped = new Uint8Array(await subtle().encrypt({ name: 'AES-GCM', iv }, kek, raw));
  return { iv: toBase64(iv), wrapped: toBase64(wrapped) };
}

async function wrapWithPassphrase(
  raw: Uint8Array<ArrayBuffer>,
  passphrase: string,
  iterations: number,
) {
  const salt = random(16);
  const kek = await passphraseKek(passphrase, salt, iterations);
  return {
    kdf: 'PBKDF2-SHA256',
    iterations,
    salt: toBase64(salt),
    ...(await wrap(kek, raw)),
  } satisfies PassphraseKey;
}

async function wrapWithRecovery(raw: Uint8Array<ArrayBuffer>, recovery: Uint8Array<ArrayBuffer>) {
  const salt = random(SALT);
  const kek = await recoveryKek(recovery, salt);
  return {
    kdf: 'HKDF-SHA256',
    salt: toBase64(salt),
    ...(await wrap(kek, raw)),
  } satisfies RecoveryKey;
}

/** Thrown when the passphrase or recovery key does not open the vault. */
export class WrongSecret extends Error {
  constructor() {
    super('Passphrase oder Wiederherstellungsschlüssel stimmt nicht');
  }
}

/** The raw vault key; WrongSecret when the secret does not open it. */
async function openKey(keys: VaultKeys, secret: Secret): Promise<Uint8Array<ArrayBuffer>> {
  let kek: CryptoKey;
  let wrapped: { iv: string; wrapped: string };
  if ('passphrase' in secret) {
    const p = keys.passphrase;
    if (p.kdf !== 'PBKDF2-SHA256' || !(p.iterations >= 100_000)) {
      throw new Error('Unbekanntes Schlüsselformat');
    }
    kek = await passphraseKek(secret.passphrase, fromBase64(p.salt), p.iterations);
    wrapped = p;
  } else {
    const recovery = parseRecovery(secret.recovery);
    if (!recovery) {
      throw new WrongSecret();
    }
    kek = await recoveryKek(recovery, fromBase64(keys.recovery.salt));
    wrapped = keys.recovery;
  }
  try {
    const raw = await subtle().decrypt(
      { name: 'AES-GCM', iv: fromBase64(wrapped.iv) },
      kek,
      fromBase64(wrapped.wrapped),
    );
    return new Uint8Array(raw);
  } catch {
    throw new WrongSecret();
  }
}

async function proofOf(raw: Uint8Array<ArrayBuffer>): Promise<string> {
  const base = await subtle().importKey('raw', raw, 'HKDF', false, ['deriveBits']);
  const bits = await subtle().deriveBits(
    {
      name: 'HKDF',
      hash: 'SHA-256',
      salt: new Uint8Array(0),
      info: enc.encode('data-den vault proof'),
    },
    base,
    256,
  );
  return toBase64(new Uint8Array(bits));
}

async function vaultOf(raw: Uint8Array<ArrayBuffer>): Promise<Vault> {
  const master = await subtle().importKey('raw', raw, 'HKDF', false, ['deriveKey', 'deriveBits']);
  const names = await subtle().deriveKey(
    {
      name: 'HKDF',
      hash: 'SHA-256',
      salt: new Uint8Array(0),
      info: enc.encode('data-den vault names'),
    },
    master,
    { name: 'AES-GCM', length: 256 },
    false,
    ['encrypt', 'decrypt'],
  );
  return { master, names };
}

/** What the server needs for new wrapped keys. */
export interface NewKeys {
  keys: VaultKeys;
  proof: string;
}

/** A new vault: its keys, the recovery key to write down, and the vault itself. */
export async function createVault(
  passphrase: string,
  iterations = PBKDF2_ITERATIONS,
): Promise<NewKeys & { recovery: string; vault: Vault }> {
  const raw = random(32);
  const recovery = random(32);
  const keys: VaultKeys = {
    version: 1,
    passphrase: await wrapWithPassphrase(raw, passphrase, iterations),
    recovery: await wrapWithRecovery(raw, recovery),
  };
  const result = {
    keys,
    proof: await proofOf(raw),
    recovery: formatRecovery(recovery),
    vault: await vaultOf(raw),
  };
  raw.fill(0);
  recovery.fill(0);
  return result;
}

/** Opens the vault with the passphrase or the recovery key. */
export async function unlock(keys: VaultKeys, secret: Secret): Promise<Vault> {
  const raw = await openKey(keys, secret);
  try {
    return await vaultOf(raw);
  } finally {
    raw.fill(0);
  }
}

/** The keys with a new passphrase; the recovery key stays. */
export async function changePassphrase(
  keys: VaultKeys,
  secret: Secret,
  passphrase: string,
  iterations = PBKDF2_ITERATIONS,
): Promise<NewKeys> {
  const raw = await openKey(keys, secret);
  try {
    return {
      keys: { ...keys, passphrase: await wrapWithPassphrase(raw, passphrase, iterations) },
      proof: await proofOf(raw),
    };
  } finally {
    raw.fill(0);
  }
}

/** The keys with a new recovery key, which replaces the old one. */
export async function newRecovery(
  keys: VaultKeys,
  secret: Secret,
): Promise<NewKeys & { recovery: string }> {
  const raw = await openKey(keys, secret);
  const recovery = random(32);
  try {
    return {
      keys: { ...keys, recovery: await wrapWithRecovery(raw, recovery) },
      proof: await proofOf(raw),
      recovery: formatRecovery(recovery),
    };
  } finally {
    raw.fill(0);
    recovery.fill(0);
  }
}

/** The name sealed for the server; throws when it is too long. */
export async function sealName(vault: Vault, name: string): Promise<string> {
  const bytes = enc.encode(name);
  if (bytes.length === 0 || bytes.length > MAX_NAME_BYTES) {
    throw new Error(`Namen im Tresor dürfen höchstens ${MAX_NAME_BYTES} Bytes lang sein`);
  }
  const iv = random(NONCE);
  const sealed = new Uint8Array(
    await subtle().encrypt({ name: 'AES-GCM', iv }, vault.names, bytes),
  );
  const out = new Uint8Array(NONCE + sealed.length);
  out.set(iv);
  out.set(sealed, NONCE);
  return toBase64Url(out);
}

/** The name behind a sealed one; null when it does not open. */
export async function openName(vault: Vault, sealed: string): Promise<string | null> {
  try {
    const bytes = fromBase64Url(sealed);
    const plain = await subtle().decrypt(
      { name: 'AES-GCM', iv: bytes.subarray(0, NONCE) },
      vault.names,
      bytes.subarray(NONCE),
    );
    return dec.decode(plain);
  } catch {
    return null;
  }
}

function fileKey(vault: Vault, salt: Uint8Array<ArrayBuffer>, usage: KeyUsage): Promise<CryptoKey> {
  return subtle().deriveKey(
    { name: 'HKDF', hash: 'SHA-256', salt, info: enc.encode('data-den vault file') },
    vault.master,
    { name: 'AES-GCM', length: 256 },
    false,
    [usage],
  );
}

function chunkNonce(counter: number, last: boolean): Uint8Array<ArrayBuffer> {
  const nonce = new Uint8Array(NONCE);
  new DataView(nonce.buffer).setUint32(7, counter);
  nonce[11] = last ? 1 : 0;
  return nonce;
}

/** The file encrypted, as the server stores it. */
export async function sealFile(vault: Vault, file: Blob): Promise<Blob> {
  const salt = random(SALT);
  const key = await fileKey(vault, salt, 'encrypt');
  const parts: BlobPart[] = [enc.encode(MAGIC), salt];
  let counter = 0;
  for (let start = 0; ; start += CHUNK) {
    const end = Math.min(start + CHUNK, file.size);
    const last = end >= file.size;
    const plain = await file.slice(start, end).arrayBuffer();
    parts.push(
      await subtle().encrypt({ name: 'AES-GCM', iv: chunkNonce(counter++, last) }, key, plain),
    );
    if (last) {
      return new Blob(parts, { type: 'application/octet-stream' });
    }
  }
}

/** The size of the file before it was sealed, from the sealed size. */
export function plainSize(sealed: number): number {
  const body = sealed - MAGIC.length - SALT;
  return Math.max(0, body - TAG * Math.ceil(body / (CHUNK + TAG)));
}

/** Thrown when a file is damaged, cut off or not from this vault. */
export class Damaged extends Error {
  constructor() {
    super('Die Datei lässt sich nicht entschlüsseln: beschädigt oder aus einem anderen Tresor');
  }
}

/** The file decrypted; Damaged when it does not decrypt in full. */
export async function openFile(vault: Vault, sealed: Blob, type = ''): Promise<Blob> {
  const header = new Uint8Array(await sealed.slice(0, MAGIC.length + SALT).arrayBuffer());
  if (
    sealed.size < MAGIC.length + SALT + TAG ||
    dec.decode(header.subarray(0, MAGIC.length)) !== MAGIC
  ) {
    throw new Damaged();
  }
  const key = await fileKey(vault, header.slice(MAGIC.length), 'decrypt');
  const parts: BlobPart[] = [];
  let counter = 0;
  for (let start = MAGIC.length + SALT; start < sealed.size; start += CHUNK + TAG) {
    const end = Math.min(start + CHUNK + TAG, sealed.size);
    const last = end === sealed.size;
    const chunk = await sealed.slice(start, end).arrayBuffer();
    try {
      parts.push(
        await subtle().decrypt({ name: 'AES-GCM', iv: chunkNonce(counter++, last) }, key, chunk),
      );
    } catch {
      throw new Damaged();
    }
  }
  return new Blob(parts, { type });
}
