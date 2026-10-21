# Changelog

GitHub Releases is the authoritative source for published release notes and
version history. The [status JSON schema](docs/status-json.md) has its own
contract version, independent of application releases. The separate
[project JSON schema](README.md#project-json) starts at version 1. This file links
the latest published release, compares it with
the current branch, and preserves notes from the repository's earlier versioning
scheme.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project uses semantic versioning.

## [Unreleased]

## [0.22.0] - 2026-10-21

- feat: add versioned JSON output to status (#231)
- docs: record v0.21.2 release (#229)

## [0.21.2] - 2026-10-19

- chore(deps): bump the go-modules group across 1 directory with 5 updates (#225)
- chore(deps): bump the github-actions group with 4 updates (#226)
- docs: record v0.21.1 release (#228)

## [0.21.1] - 2026-10-19

- fix(ci): recognize Codex receipts with HTML metadata (#227)
- docs: record v0.21.0 release (#224)

## [0.21.0] - 2026-10-10

- feat: stream status results with loading feedback (#223)
- docs: record v0.20.0 release (#221)

## [0.20.0] - 2026-10-10

- feat: mark synced clean default branches in status (#219) (#220)
- docs: record v0.19.5 release (#218)

## [0.19.5] - 2026-10-10

- perf: reduce status latency with reliable section caching (#217)

## [0.19.4] - 2026-10-09

- fix: use canonical organization GitHub destinations (#215)
- docs: record v0.19.3 release (#213)

## [0.19.3] - 2026-10-08

- docs: record v0.19.2 release (#212)
- fix: allow thirty minutes for Codex reviews (#211)

## [0.19.2] - 2026-10-08

- chore(deps): bump github/gh-aw-actions/setup-cli (#209)
- fix: make monitor All repos cover the account and organizations (#201)

## [0.19.1] - 2026-10-08

- fix: restore merged status after repository moves (#210)
- docs: record v0.19.0 release (#208)

## [0.19.0] - 2026-10-08

- feat: show stash counts and aligned clean status rows (#206)
- docs: record v0.18.2 release (#207)

## [0.18.2] - 2026-10-08

- fix: preserve OAuth scope arrays in credential checks (#204)
- docs: record v0.18.1 release (#203)

## [0.18.1] - 2026-10-07

- ci: add read-only migration credential checks (#202)
- docs: record v0.18.0 release (#200)

## [0.18.0] - 2026-10-07

- feat: rewrite monitor with a responsive color dashboard (#198) (#199)
- docs: record v0.17.0 release (#197)

## [0.17.0] - 2026-10-07

- feat: add global --clear terminal output flag (#196)
- docs: record v0.16.0 release (#194)

## [0.16.0] - 2026-10-07

- feat: include open Dependabot PRs in pr me (#192) (#193)
- docs: record v0.15.7 release (#191)

## [0.15.7] - 2026-10-07

- fix: discard dashboard theme previews on native cancellation (#190)
- docs: record v0.15.6 release (#189)

## [0.15.6] - 2026-10-07

- ci: qualify standalone dashboard launcher on Windows (#188)
- docs: record v0.15.5 release (#187)

## [0.15.5] - 2026-10-06

- chore: enforce dashboard JavaScript quality baselines (#186)
- docs: record v0.15.4 release (#185)

## [0.15.4] - 2026-10-06

- fix: preserve dashboard session keyboard focus across refreshes (#184)
- docs: record v0.15.3 release (#183)

## [0.15.3] - 2026-10-06

- test: isolate status cache authentication fixtures (#175) (#182)
- docs: record v0.15.2 release (#181)

## [0.15.2] - 2026-10-06

- chore: apply Go quality gates to release and review helpers (#180)
- docs: record v0.15.1 release (#173)

## [0.15.1] - 2026-10-06

- chore(deps): bump github/gh-aw-actions/setup-cli from 0.89.21 to 0.90.1 in the github-actions group (#169)
- docs: record v0.15.0 release (#172)

## [0.15.0] - 2026-10-06

- feat: add gold gh-x project icon for T3 Code (#171)
- docs: record v0.14.6 release (#168)

## [0.14.6] - 2026-10-04

- fix: verify dashboard hub Tailscale publication (#167)
- docs: record v0.14.5 release (#166)

## [0.14.5] - 2026-10-04

- fix: prevent stale dashboard refreshes (#165)
- docs: record v0.14.4 release (#162)

## [0.14.4] - 2026-10-01

- chore(deps): bump the github-actions group with 3 updates (#160)
- docs: record v0.14.3 release (#161)

## [0.14.3] - 2026-10-01

- chore(deps): bump charm.land/bubbletea/v2 in the go-modules group (#159)
- docs: record v0.14.2 release (#158)

## [0.14.2] - 2026-09-23

- chore: align gh-aw CLI with GitHub Actions update (#153)
- docs: record v0.14.1 release (#157)

## [0.14.1] - 2026-09-23

- chore(deps): bump github.com/cli/go-gh/v2 in the go-modules group (#143)
- docs: record v0.14.0 release without duplicate Unreleased note (#156)

## [0.14.0] - 2026-09-23

- feat: cache GitHub status data per repository (#152)
- docs: record v0.13.12 release without duplicate entry (#151) (#154)

## [0.13.12] - 2026-09-22

- Show approvers and approval times in open PR lists (#150)
- docs: record v0.13.11 release (#148)

## [0.13.11] - 2026-09-21

- fix: retry empty private repository searches (#146)
- docs: record v0.13.10 release (#141)

## [0.13.10] - 2026-09-20

- Show recent merged pull requests in status (#140)
- docs: record issue #135 quality audit (#138)
- docs: record v0.13.9 release (#137)

## [0.13.9] - 2026-09-20

- fix: honor Copilot review verdicts (#135) (#136)
- docs: record v0.13.8 release (#134)

## [0.13.8] - 2026-09-18

- fix: complete local audit qualification (#130) (#133)
- docs: record v0.13.7 release (#132)

## [0.13.7] - 2026-09-18

- fix: isolate malformed dashboard requests (#131)
- docs: record v0.13.6 release (#128)

## [0.13.6] - 2026-09-17

- chore(deps): bump gh-aw setup CLI to v0.89.7 (#126)
- docs: record v0.13.5 release (#127)

## [0.13.5] - 2026-09-17

- chore(deps): bump github.com/mattn/go-runewidth in the go-modules group (#124)
- docs: record issue #120 completion history (#123)
- docs: record v0.13.4 release (#122)

## [0.13.4] - 2026-09-14

- Show parent and sub-issue progress across issue views (#121)
- docs: record v0.13.3 release (#119)

## [0.13.3] - 2026-09-12

- fix: bound and cancel GitHub subprocesses (#118)
- docs: record v0.13.2 release (#117)

## [0.13.2] - 2026-09-12

- ci: build every release target before merge (#116)
- docs: record v0.13.1 release (#115)

## [0.13.1] - 2026-09-12

- ci: enforce mutator coverage floor (#114)
- docs: record v0.13.0 release (#113)

## [0.13.0] - 2026-09-12

- feat: publish provenance for release binaries (#112)
- docs: record v0.12.8 release (#111)

## [0.12.8] - 2026-09-12

- fix: isolate authoritative changelog review runs (#110)
- docs: record v0.12.7 release (#109)

## [0.12.7] - 2026-09-12

- docs: record v0.12.6 release (#108)
- fix: require current-head Codex review in CI (#106)

## [0.12.6] - 2026-09-12

- fix: complete Codex review evidence compatibility (#107)
- docs: record v0.12.5 release (#105)

## [0.12.5] - 2026-09-12

- refactor: prepare generic Codex review verification (#104)
- docs: record v0.12.4 release (#97)

## [0.12.4] - 2026-09-12

- chore(deps): bump the github-actions group across 1 directory with 3 updates (#95)
- docs: record v0.12.3 release (#96)

## [0.12.3] - 2026-09-12

- chore(deps): bump the go-modules group across 1 directory with 3 updates (#94)
- docs: record v0.12.2 release (#93)

## [0.12.2] - 2026-09-12

- fix: resolve SSH aliases for status API routing (#92)
- docs: record v0.12.1 release (#90)

## [0.12.1] - 2026-09-09

- fix: restore available supplemental data with fail-closed diagnostics (#88) (#89)
- docs: record v0.12.0 release (#85)

## [0.12.0] - 2026-09-06

- feat: show local date and time in status header (#84)
- docs: record v0.11.10 release (#83)

## [0.11.10] - 2026-09-06

- test: establish critical CLI performance budgets (#63) (#82)
- docs: record v0.11.9 release (#81)

## [0.11.9] - 2026-09-06

- fix: stabilize changelog reviews with connected Codex (#80)

## [0.11.8] - 2026-09-05

- fix: guard release changelog auto-merge with current reviews (#77)
- docs: record v0.11.7 release (#76)

## [0.11.7] - 2026-09-05

- fix: preserve Unicode separators in Codex rollout JSONL (#75)
- docs: record v0.11.6 release (#74)

## [0.11.6] - 2026-09-05

- chore: pin GitHub Actions dependencies to immutable commits (#73)
- docs: record v0.11.5 release (#71)

## [0.11.5] - 2026-09-05

- fix: support CRLF changelog validation (#69)

## [0.11.4] - 2026-09-05

- chore: require pull requests for main (#66)
- docs: record v0.11.3 release (#68)

## [0.11.3] - 2026-09-05

- docs: reconcile changelog with published releases (#67)

## [0.11.2] - 2026-09-05

### Changed

- Required CodeQL analysis and dependency review in the repository's CI quality
  gate.

## Legacy history

The entries below came from an earlier versioning scheme and are retained as
historical notes without version links. They do not describe later GitHub
Releases that reuse the same version numbers. Their dates distinguish them from
the published releases above.

### 0.18.0 legacy entry (2026-06-08)

#### Release notes

- Added `gh x pr review [number]` for read-only agentic PR review using
  configurable CLI providers.
- Supports provider presets for Codex, Claude Code, GitHub Copilot CLI,
  Gemini CLI, OpenCode, plus custom command templates.
- Added top-level `gh x changelog [n]`.
- Shows the latest release changelog by default, or the latest `n` release
  changelogs when a count is supplied.

### 0.17.0 legacy entry (2026-06-06)

#### Improved

- Improved `gh x workflow list` trigger labels so scheduled workflows show
  readable UTC schedule phrases instead of raw cron expressions.
- Added readable formatting for hourly schedules such as `0 * * * *`.

#### New

- Added `gh x status` for a compact git and GitHub repository summary.
- Shows upstream sync state, working tree change counts, dangling local branch
  count, open issue count, and open pull request count.
- Renamed `workflow_dispatch` trigger output to `manual`.
- Renamed `workflow_run` trigger output to `after workflow run` to distinguish
  dependent workflow triggers from manual triggers.

### 0.16.0 legacy entry (2026-06-06)

#### Added

- Added a `TRIGGERS` column to `gh x workflow list`.
- Shows common GitHub Actions triggers such as `push`, `pull_request`, and
  `workflow_dispatch`.
- Shows schedule cron expressions inline, for example
  `schedule: 15 6 * * 1-5`.
- Shows useful trigger filters for branch and pull request event types, such as
  `branches: main` and `types: opened, synchronize, reopened`.

#### Changed

- Workflow list output now enriches GitHub workflow metadata from workflow YAML
  definitions when available.
- Dynamic or unreadable workflow definitions now display `unknown` in the
  trigger column instead of failing the list command.

[Unreleased]: https://github.com/hemsoft-dev/gh-x/compare/v0.22.0...HEAD
[0.22.0]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.22.0
[0.21.2]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.21.2
[0.21.1]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.21.1
[0.21.0]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.21.0
[0.20.0]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.20.0
[0.19.5]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.19.5
[0.19.4]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.19.4
[0.19.3]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.19.3
[0.19.2]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.19.2
[0.19.1]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.19.1
[0.19.0]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.19.0
[0.18.2]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.18.2
[0.18.1]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.18.1
[0.18.0]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.18.0
[0.17.0]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.17.0
[0.16.0]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.16.0
[0.15.7]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.15.7
[0.15.6]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.15.6
[0.15.5]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.15.5
[0.15.4]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.15.4
[0.15.3]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.15.3
[0.15.2]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.15.2
[0.15.1]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.15.1
[0.15.0]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.15.0
[0.14.6]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.14.6
[0.14.5]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.14.5
[0.14.4]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.14.4
[0.14.3]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.14.3
[0.14.2]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.14.2
[0.14.1]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.14.1
[0.14.0]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.14.0
[0.13.12]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.13.12
[0.13.11]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.13.11
[0.13.10]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.13.10
[0.13.9]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.13.9
[0.13.8]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.13.8
[0.13.7]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.13.7
[0.13.6]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.13.6
[0.13.5]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.13.5
[0.13.4]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.13.4
[0.13.3]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.13.3
[0.13.2]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.13.2
[0.13.1]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.13.1
[0.13.0]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.13.0
[0.12.8]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.12.8
[0.12.7]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.12.7
[0.12.6]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.12.6
[0.12.5]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.12.5
[0.12.4]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.12.4
[0.12.3]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.12.3
[0.12.2]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.12.2
[0.12.1]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.12.1
[0.12.0]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.12.0
[0.11.10]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.11.10
[0.11.9]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.11.9
[0.11.8]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.11.8
[0.11.7]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.11.7
[0.11.6]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.11.6
[0.11.5]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.11.5
[0.11.4]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.11.4
[0.11.3]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.11.3
[0.11.2]: https://github.com/hemsoft-dev/gh-x/releases/tag/v0.11.2
