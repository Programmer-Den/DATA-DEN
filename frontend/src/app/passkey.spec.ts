import {
  creationOptions,
  fromBase64Url,
  passkeyError,
  requestOptions,
  toBase64Url,
} from './passkey';

describe('passkey', () => {
  it('converts base64url both ways', () => {
    const bytes = new Uint8Array([0, 251, 255, 62, 63]);
    const text = toBase64Url(bytes);
    expect(text).toBe('APv_Pj8');
    expect(new Uint8Array(fromBase64Url(text))).toEqual(bytes);
  });

  it('turns the server options into bytes for the browser', () => {
    const create = creationOptions({
      challenge: 'AQ',
      rp: { id: 'nas.example.de', name: 'DATA-DEN' },
      user: { id: 'Ag', name: 'chriszly', displayName: 'chriszly' },
      excludeCredentials: [{ type: 'public-key', id: 'Aw' }],
    });
    expect(new Uint8Array(create.challenge as ArrayBuffer)).toEqual(new Uint8Array([1]));
    expect(new Uint8Array(create.user.id as ArrayBuffer)).toEqual(new Uint8Array([2]));
    expect(new Uint8Array(create.excludeCredentials![0].id as ArrayBuffer)).toEqual(
      new Uint8Array([3]),
    );
    expect(create.rp.id).toBe('nas.example.de');

    const get = requestOptions({ challenge: 'BA', userVerification: 'required' });
    expect(new Uint8Array(get.challenge as ArrayBuffer)).toEqual(new Uint8Array([4]));
    expect(get.allowCredentials).toBeUndefined();
    expect(get.userVerification).toBe('required');
  });

  it('stays quiet when the prompt was closed', () => {
    expect(passkeyError(new DOMException('closed', 'NotAllowedError'))).toBeNull();
    expect(passkeyError(new DOMException('twice', 'InvalidStateError'))).toContain('schon');
    expect(passkeyError(new Error('kaputt'))).toBe('kaputt');
  });
});
