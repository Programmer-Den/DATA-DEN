/**
 * Passkeys in the browser: the server sends the options for
 * navigator.credentials as JSON with base64url strings, the browser wants
 * bytes, and the answer goes back as JSON again.
 */

/** JSON as the server sends and expects it. */
type Json = Record<string, unknown>;

/** Whether this browser can use passkeys at all (it needs HTTPS too). */
export function passkeysSupported(): boolean {
  return typeof window !== 'undefined' && window.isSecureContext && 'PublicKeyCredential' in window;
}

export function fromBase64Url(value: string): ArrayBuffer {
  const base64 = value.replace(/-/g, '+').replace(/_/g, '/');
  const binary = atob(base64.padEnd(base64.length + ((4 - (base64.length % 4)) % 4), '='));
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) {
    bytes[i] = binary.charCodeAt(i);
  }
  return bytes.buffer;
}

export function toBase64Url(value: ArrayBuffer | ArrayBufferView): string {
  const bytes =
    value instanceof ArrayBuffer
      ? new Uint8Array(value)
      : new Uint8Array(value.buffer, value.byteOffset, value.byteLength);
  let binary = '';
  for (const b of bytes) {
    binary += String.fromCharCode(b);
  }
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

function descriptors(list: unknown): PublicKeyCredentialDescriptor[] | undefined {
  if (!Array.isArray(list)) {
    return undefined;
  }
  return list.map((d: Json) => ({
    ...d,
    type: 'public-key',
    id: fromBase64Url(d['id'] as string),
  })) as PublicKeyCredentialDescriptor[];
}

/** The options for navigator.credentials.create, from the server's JSON. */
export function creationOptions(json: Json): PublicKeyCredentialCreationOptions {
  const user = json['user'] as Json;
  return {
    ...json,
    challenge: fromBase64Url(json['challenge'] as string),
    user: { ...user, id: fromBase64Url(user['id'] as string) },
    excludeCredentials: descriptors(json['excludeCredentials']),
  } as unknown as PublicKeyCredentialCreationOptions;
}

/** The options for navigator.credentials.get, from the server's JSON. */
export function requestOptions(json: Json): PublicKeyCredentialRequestOptions {
  return {
    ...json,
    challenge: fromBase64Url(json['challenge'] as string),
    allowCredentials: descriptors(json['allowCredentials']),
  } as unknown as PublicKeyCredentialRequestOptions;
}

/** A credential from the browser as the JSON the server reads. */
export function credentialJson(credential: PublicKeyCredential): Json {
  const response = credential.response;
  const out: Json = {};
  out['clientDataJSON'] = toBase64Url(response.clientDataJSON);
  if ('attestationObject' in response) {
    const attestation = response as AuthenticatorAttestationResponse;
    out['attestationObject'] = toBase64Url(attestation.attestationObject);
    if (typeof attestation.getTransports === 'function') {
      out['transports'] = attestation.getTransports();
    }
  } else {
    const assertion = response as AuthenticatorAssertionResponse;
    out['authenticatorData'] = toBase64Url(assertion.authenticatorData);
    out['signature'] = toBase64Url(assertion.signature);
    if (assertion.userHandle) {
      out['userHandle'] = toBase64Url(assertion.userHandle);
    }
  }
  return {
    id: credential.id,
    rawId: toBase64Url(credential.rawId),
    type: credential.type,
    authenticatorAttachment: credential.authenticatorAttachment ?? undefined,
    clientExtensionResults: credential.getClientExtensionResults(),
    response: out,
  };
}

/** Creates a passkey with the server's options and returns the answer for it. */
export async function createPasskey(options: Json): Promise<Json> {
  const credential = await navigator.credentials.create({ publicKey: creationOptions(options) });
  if (!credential) {
    throw new Error('Kein Passkey angelegt');
  }
  return credentialJson(credential as PublicKeyCredential);
}

/** Signs the server's challenge with a passkey and returns the answer for it. */
export async function getPasskey(options: Json): Promise<Json> {
  const credential = await navigator.credentials.get({ publicKey: requestOptions(options) });
  if (!credential) {
    throw new Error('Kein Passkey gewählt');
  }
  return credentialJson(credential as PublicKeyCredential);
}

/** A message for a failed or cancelled passkey prompt, or null to show none. */
export function passkeyError(err: unknown): string | null {
  if (err instanceof DOMException) {
    // The person closed the prompt or took too long: nothing to report.
    if (err.name === 'NotAllowedError' || err.name === 'AbortError') {
      return null;
    }
    if (err.name === 'InvalidStateError') {
      return 'Dieses Gerät hat schon einen Passkey für das Konto';
    }
    if (err.name === 'SecurityError') {
      return 'Passkeys gehen nur unter der eingerichteten Adresse (DATA_DEN_ORIGIN)';
    }
  }
  return err instanceof Error ? err.message : 'Passkey fehlgeschlagen';
}
