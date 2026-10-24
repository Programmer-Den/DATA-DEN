# Light runners

Two self-hosted GitHub Actions runners, `data-den-light-1` and `-2`, as Linux
containers under Docker Desktop on the Windows PC (`runner.compose.yaml`).
They have the labels `linux` and `light` and run the checks, the Go and the
Angular jobs of CI and the PR checks. The Docker image is always built on
GitHub's own runners, as these containers have no Docker.

Until the runners are set up, every job runs on GitHub's runners. Once they
are, the repository variable `LIGHT_RUNNER` moves the jobs over.

## Setup

1. Start Docker Desktop with Linux containers.
2. On GitHub: **Settings › Actions › Runners › New self-hosted runner**. Copy
   the value after `--token` (valid for one hour, enough for both runners). Do
   not run the other commands shown there.
3. In the repository folder:

   ```powershell
   powershell -ExecutionPolicy Bypass -File runner/register-runner.ps1
   ```

   The script asks for the token without showing it and starts the
   containers. The logs should show `Listening for Jobs`, and both runners
   appear as *Idle* under Settings › Actions › Runners.
4. **Settings › Secrets and variables › Actions › Variables › New repository
   variable**: name `LIGHT_RUNNER`, value `["self-hosted", "light"]`. Delete
   the variable to go back to GitHub's runners.

The registrations live in volumes, so the containers come back with Docker
Desktop (`restart: unless-stopped`) without a new token.

## Commands

```powershell
docker compose -f runner/runner.compose.yaml logs -f   # logs
docker compose -f runner/runner.compose.yaml down      # stop
docker compose -f runner/runner.compose.yaml up -d     # start again
```

## Register again

Remove the old runner under Settings › Actions › Runners, then:

```powershell
docker compose -f runner/runner.compose.yaml down
docker volume rm data-den-runner_runner-light-1-data data-den-runner_runner-light-2-data
powershell -ExecutionPolicy Bypass -File runner/register-runner.ps1
```

## Security

The repository is public, so a pull request from a fork could run code on
these runners. **Settings › Actions › General › Approval for running fork
pull request workflows from contributors** must stay at *Require approval for
all external contributors*; approve a fork's run only after reading its
changes. Neither container has the Docker socket or a folder of the PC, only the
read-only `runner-cleanup.sh`, which deletes the checkout after every job.
