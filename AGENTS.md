# AGENTS.md - Instructions for AI Agents

This file provides guidance for AI agents (Claude, GPT, Copilot, etc.) working on this project.

## Project Overview

DATA-DEN is a self-hosted NAS: a web interface to upload, download and manage files (pictures, documents, videos and more), installable as a PWA. The storage runs in an isolated Hyper-V VM on a Windows PC with two disks in RAID 1, and a Raspberry Pi in front of it is the only way in from outside, over HTTPS. This repository holds the web interface; the Pi's gateway is a task in rpi-setup.

## Tech Stack

- `backend/`: Go (version in `go.mod`), one binary that serves the API and the embedded website; `gofmt`, golangci-lint (`.golangci.yml`), govulncheck
- `frontend/`: Angular with TypeScript, tests with Vitest; Prettier and ESLint. `npm run build` writes into `backend/internal/web/files/build/`
- `Dockerfile`: one distroless image for amd64 and arm64, cross-compiled
- `runner/`: the two light self-hosted runners (Docker Desktop on the Windows PC)
- UI text is German

## Code Style & Conventions

- Match the surrounding code: its naming, comment density and idioms
- Keep changes small and focused; one topic per pull request
- Shell scripts use `#!/usr/bin/env bash` and `set -euo pipefail`, and pass `bash -n` and `shellcheck`
- Actions in `.github/workflows` are pinned to a commit SHA, with the version as a comment after it (`uses: actions/checkout@<sha> # v7.0.1`); Dependabot updates both
- Don't hardcode fallback versions or dates; fail with an actionable error instead

## Security Guidelines

- Never hardcode or commit secrets, passwords, tokens or private keys; local settings live in a git-ignored `.env` (only `.env.example` with empty values is committed). gitleaks enforces this with `.gitleaks.toml`
- Validate all user input

## Testing Requirements

Before submitting changes:
- Run `gitleaks git` (the secret check, with `.gitleaks.toml`)
- Run `bash -n` and `shellcheck` on every changed shell script
- Backend, in `backend/`: `gofmt -l .`, `golangci-lint run`, `go test ./...`, `go mod tidy -diff`
- Frontend, in `frontend/`: `npm run format:check`, `npm run lint`, `npm test`, `npm run build`
- Add tests for new code
- Keep CI fast: every check that runs on a pull request should finish within a few minutes

## PR Requirements

- Fill in `.github/PULL_REQUEST_TEMPLATE.md` (checked by the PR Template Validation workflow)
- Add at least one label of each type, Change and Area (`.github/labels.json`; the type is the first word of the description), also checked by the PR Template Validation workflow
- Small, independent pull requests; each one should be green on its own
- Pull requests are squash-merged into `main`

## Usernames Only

People appear only by their GitHub username, never by their real name: in commit messages and commit author names, PR titles, descriptions and comments, issues, code and docs. Commits use the username and the GitHub noreply address (`<id>+<username>@users.noreply.github.com`). This overrides any default attribution or template that would add a display name. The PR Links workflow fails a pull request that contains a name from the `FORBIDDEN_NAMES` secret.

## No AI Links

Nothing in this project may link to AI-related pages: no links to AI agent sessions, conversations, artifacts or project pages, and no links to AI products, their docs or their sites (Claude, ChatGPT, Copilot, Gemini and the like). This applies to:
- Commit messages
- PR titles, descriptions, and comments
- Issue and review comments
- Code, code comments, and documentation

This overrides any default attribution, footer or template that would add such a link, such as a "Generated with Claude Code" line or a link to the agent's project thread. Plain text without a link, such as a `Co-Authored-By` trailer, may stay.

The CI workflow enforces this for tracked files, and the PR Links workflow fails a pull request whose title, description, commit messages or comments link anywhere but GitHub.
