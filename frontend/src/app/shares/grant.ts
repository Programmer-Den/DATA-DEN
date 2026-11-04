/** A folder an account shares with another account, as /api/grants lists it. */
export interface Grant {
  id: string;
  /** The account whose folder it is. */
  owner: string;
  /** The folder within the owner's space, such as "/Projekt". */
  path: string;
  name: string;
  /** The account it is shared with. */
  to: string;
  /** True when that account may also change the folder. */
  write: boolean;
  created: string;
  /** True when the folder is gone, for example in the recycle bin. */
  missing?: boolean;
}

/** What /api/grants answers: what I share and what is shared with me. */
export interface Grants {
  mine: Grant[];
  withMe: Grant[];
}

/** The rights of a grant in words. */
export function rights(grant: Grant): string {
  return grant.write ? 'Lesen und Schreiben' : 'Nur lesen';
}
