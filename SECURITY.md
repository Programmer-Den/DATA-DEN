# Security

## Reporting a vulnerability

Please do not open a public issue for a security problem. Report it privately
through the repository's **Security** tab, under **Report a vulnerability**.
You will get an answer there, and the problem is fixed before it is made public.

## Who can change the code

Only the maintainer can push to this repository. Everyone else contributes
through a pull request from a fork:

- `main` cannot be pushed to directly, force-pushed or deleted
- every pull request needs the maintainer's review (see `.github/CODEOWNERS`)
  and green checks before it is squash-merged
- workflows from a pull request by an outside contributor only run after
  the maintainer approves them, and they get read-only access to the repository
