# repo-base

Template for new repositories and home of the shared, reusable workflows. The checks follow den-watcher and rpi-setup, the issue forms, labels and Projects follow game-proto.

## New repository from the template

GitHub → **New repository** → *Repository template* `Programmer-Den/repo-base`. Then:

1. Delete `.github/workflows/reusable-*.yml`; the new repository calls them here.
2. Fill in `README.md` and the open sections of `AGENTS.md`.
3. Add the areas of the repository to `.github/labels.json` (description `Area: ...`) and the same names to the Area dropdown of both forms in `.github/ISSUE_TEMPLATE/`, before `not sure`; raise each form's `default` by one so it still preselects `not sure`.
4. In `.github/projects.json`, change `repo-base` in the title to the repository's name; add a Project with `"label": "<area>"` for any area that gets its own board.
5. Uncomment the ecosystems of the repository in `.github/dependabot.yml`.
6. Run the **Labels** workflow once by hand.
7. Add the repository secret `PROJECTS_TOKEN` (a classic token with only the `project` scope), start **Project setup** by hand, then finish each new Project on GitHub: turn on its Auto-add workflow (filter `is:issue,pr is:open`, plus `label:<area>` for an area's Project), set its "Item added to project" workflow to Issue only with the status Todo, and link it to the repository if the run summary says so. Without the secret the Project workflows do nothing.
8. Add a ruleset "Protect main": pull request with review, required checks `CI / Checks / Secret and link check`, `PR Links / PR Links / github-links-only` and `PR Template Validation / PR Template / validate-pr-template`, no force push or deletion. On a private repository this needs GitHub Pro.

## What comes with it

| File | Purpose |
|---|---|
| `.github/PULL_REQUEST_TEMPLATE.md` | PR template, checked by PR Template Validation |
| `.github/ISSUE_TEMPLATE/` | Issue forms for bug report and feature wish, each with its Change label and an Area dropdown; blank issues are off |
| `.github/projects.json` | The GitHub Projects (boards) of the repository, created by Project setup |
| `.github/labels.json` | Labels of the groups Change, Area, Priority and Status, synced by the Labels workflow |
| `.github/CODEOWNERS` | Every pull request asks @Chriszly for a review |
| `.github/dependabot.yml` | Weekly updates of the actions, with labels |
| `.gitleaks.toml`, `.gitleaksignore` | Secret check rules and known harmless findings |
| `AGENTS.md`, `CLAUDE.md` | Rules for agents, including "No AI Links" |
| `.claude/settings.json` | Turns off the attribution links Claude Code adds to commits and PRs |
| `SECURITY.md` | How to report a vulnerability, who can change the code |
| `.github/workflows/ci.yml`, `pr-links.yml`, `pr-template-validation.yml`, `labels.yml`, `issue-labels.yml`, `project-*.yml` | Call the shared workflows |

## Reusable workflows

| Workflow | Checks |
|---|---|
| `reusable-checks.yml` | gitleaks over the commits of the PR or push, and no links to AI sites in tracked files |
| `reusable-pr-links.yml` | Title, description, commit messages, comments and reviews link only to GitHub and the owner's GitHub Pages site |
| `reusable-pr-template-validation.yml` | The description has the template's sections and the PR has a label of every type |
| `reusable-labels.yml` | Syncs the repository's labels with `.github/labels.json`, renames through `previous` |
| `reusable-issue-labels.yml` | Adds the Area label from the issue form and files the issue into its Projects as Todo |
| `reusable-project-status.yml` | Puts each open PR into its Projects as In Progress |
| `reusable-project-setup.yml` | Creates the missing Projects of `.github/projects.json`, public, with description and README |
| `reusable-project-report.yml` | Prints every Project's items and the open issues and PRs with their Projects |

If this repository is private, so that other repositories may call these workflows, **Settings → Actions → General → Access** is set to *Accessible from repositories in the organization*.

Every workflow runs on `ubuntu-latest` by default and needs only `bash`, `curl`, `tar`, `sha256sum` and the runner's Node, no Docker or `gh`. On a light self-hosted runner:

```yaml
jobs:
  checks:
    uses: Programmer-Den/repo-base/.github/workflows/reusable-checks.yml@main
    with:
      runs-on: '["self-hosted", "light"]'
```

`reusable-pr-template-validation.yml` takes `required-sections` (one heading per line) and `required-label-types` (default Change and Area) for a repository with other headings or label groups.
