# Combined AO Visibility and Routing Hardening Installation Plan

## Scope and approval boundary

Prepare one private candidate containing the reviewed automation-visibility frontend and the complete committed routing-hardening stack. This document does not authorize production cutover.

Jason's current request explicitly requires separate current-turn approval before stopping AO, replacing the installed app, migrating live data, or running `route_9router.py apply`. Older installation consent does not satisfy this new boundary. Do not run apply/bootstrap against production as preparation.

No companion worktree edit, unconditional project-config PUT, CAS removal, lifecycle-admission weakening, generic recovery bypass, automatic cleanup of uncertain setup writers, or credential copying into config.

## Integration identity

- Installed app updated from 0.13.3 to 0.13.4 during the earlier approved graceful quit. The 0.13.3 candidate must not replace it.
- Current candidate base: official `v0.13.4`, commit `e8a77577c14b015b947057d67c9171a78cdd5099`.
- UI head before backend integration: `048c80d8b` on `deploy/hide-automation-v0134`.
- Combined branch: `prepare/hide-automation-routing-v0134` in `~/dev/worktrees/ao_hide_automation_plan`.
- Pinned backend head: `af6132514fe5c842c617e4f4285acfead60ebfaf`, branch `fix/post-create-session-context`.
- Companion source: `~/dev/worktrees/ao_postcreate_context`, read-only. It has uncommitted changes beyond the pin; those are excluded.
- Workflow consumer: `scripts/ao/route_9router.py`, merged workflow PR https://github.com/Lieberman-Family-Office/og-model-build-cursor-workflow/pull/3478, merge `a32f2ea3c86f26b11363ac40d9c2e9861e613a9f`.
- Backend contribution: https://github.com/OrchestratorInc/agent-orchestrator/pull/6365, inspected open at the pinned backend head. Its reported test results are not substituted for combined-source validation.

Complete required backend commits, in order:
1. `c38c3c6e11926e9127a373d2aa494c304d5b0946`: protected PostCreate context and early publication.
2. `d6c18130105327d8837ef89f0eb6447a1d195412`: context documentation.
3. `cc4e40e45927398b4aa998bd711bd15b59be6c22`: conservative uncertain-workspace preservation.
4. `9ac0b4bb0626b1e6c342debfbd40e9f1b967a638`: distinguish generation reservation from controller publication.
5. `6902780bf9a2be37580a1d3cb500292289f81808`: preserve unowned starts on cancellation.
6. `af6132514fe5c842c617e4f4285acfead60ebfaf`: database revision and config CAS.

The original v0.13.3 baseline is an ancestor of the companion stack's base. The installed v0.13.4 is the deployment baseline; all six cherry-picks applied cleanly there. No companion branch merge imports unrelated upstream changes beyond the installed release.

## Preparation verification

1. Re-read branch/head and installed version. Freeze the candidate source SHA only after test and plan changes are committed.
2. Validate the ownership/crash-preservation regressions through actual Spawn, hook, SQLite, and daemon-only SIGKILL paths on temporary test data.
3. Validate CAS through service, controller, independent SQLite writers, malformed/null preconditions, monotonic revision guards, archive/ABA and same-revision concurrency. Stale revision must return `409 PROJECT_REVISION_CONFLICT`; project identity/display fields must not be overwritten by config-only writes.
4. Run full backend suite with real schema-matching Codex 0.146.0, real bundled tmux, cleared inherited provider credentials and isolated test fixtures. Do not exclude failed tests. Label the selected no-credential provider context exactly; do not pretend it proves production inference.
5. Run relevant race checks, build, vet, pinned golangci-lint, sqlc/OpenAPI generation parity, full frontend tests, both typechecks, and the feature renderer E2E. Counts from a test-filter run with zero matching subjects are did-not-run, not a pass for that population.
6. Run the workflow consumer regression `tests/test_ao_route_9router.py` on its fixtures. No live `apply` or `bootstrap`.
7. Run the added `TestProjectRevisionUpgradePreservesPopulatedReleaseData`: schema 189 fixture, project/role/profile/PostCreate config, session, conversation and automation report; production `sqlite.Open` migration to combined schema; unchanged bytes/counts, revision initialized to zero, integrity and FK checks.
8. Preserve the real `.ao/claude-9router/settings.json` and `.claude.json`. Preparation reads only hashes or the minimum metadata; no key exports, profile edits or live project edits.
9. Independent combined-source review must judge integration, not merely repeat each isolated component's prior approval. Companion uncommitted edits remain outside scope; re-evaluate only if Jason supplies a new pinned commit.

## One combined artifact

- Build-only version stamp is `0.13.4`, matching the installed release. Restore `frontend/package.json` to committed source after packaging.
- Preserve the installed updater-feed target rather than silently changing update policy.
- Sign with `Developer ID Application: Biotrack Solutions LLC (LN4NJ67TXS)` and notarize through Keychain profile `AO_HIDE_AUTOMATION`.
- Use the existing compatible bundled Node build runtime; do not replace global Node/Codex or routing.
- Do not run Node unit tests concurrently with packaging; Forge rebuilds SQLite for Electron.
- No release publish/tag/feed creation. No private key or app-specific password in command arguments, committed files, reports, or logs.
- Run `frontend/scripts/verify-mac-artifact.sh` on the actual app bundle. Its codesign, Gatekeeper, staple, nested Node architecture/entitlements and JavaScript smoke must all pass.
- Record full source SHA, base tag, imported commit list, bundle version, artifact absolute path, SHA-256s for app.asar and daemon, signing team, notarization ID/status when available, verification exits and the remaining blockers.
- Any post-signing content modification voids this artifact identity; rebuild/re-sign/re-notarize. The former 127f241e3 signature does not cover this combined source.

## Production cutover plan — do not execute without new approval

### A. Preflight and updater-race prevention

1. Obtain Jason's current-turn permission for checkpoint/quit and app replacement. Obtain separate permission for live database migration, which occurs automatically when the new daemon opens `ao.db`.
2. Read current app version/source and updater status. If a different version is staged or installed, reconcile it before replacement; do not downgrade silently.
3. Ask the AO coordinator for durable maintenance checkpoints and a dispatch pause using supported `ao send`. Verify acceptance and readiness; an accepted transport receipt is not every-worker quiescence.
4. Do not mutate/stash/discard other task trees. Preserve unpushed committed work. If an uncertain setup writer is present, do not force recovery or erase the row to make maintenance green.
5. Quit gracefully only after permission/readiness. macOS ShipIt may install an already-staged release on quit even after automatic-download settings change. Let it finish; do not kill or race it.
6. Confirm app/main daemon/updater are stopped. Persistent worker hosts may survive AO quit; do not delete or kill them indiscriminately. Recheck actual installed version after ShipIt finishes.
7. If ShipIt is still active, destination identity is unreadable, or workers have uncheckpointed writes, stop. A restart is not evidence that a process ended safely.

### B. Recoverable backups

Create a unique owner-only directory under `~/.ao/backups/combined-hide-routing-<UTC timestamp>` before the new daemon starts.

Back up:
- `/Applications/Agent Orchestrator.app` using `ditto`, preserving signature metadata.
- Consistent SQLite snapshot with native `.backup`, plus schema version, FK/integrity status, project/session/conversation/report counts and row identities.
- `~/.ao/claude-9router` including settings and `.claude.json` for Context7/Foundation and existing servers.
- `~/.ao/electron` preference state after graceful app quit.
- Relevant runfile/desktop configuration needed for recovery, without exposing keys.
- Registered worktree inventory and maintenance checkpoint locators; do not copy huge deal/credential trees by default.

Treat backups as confidential: mode 0700 directory, private files, no upload/commit/chat dump. Preserve exact app and DB backup hashes.

Do not overwrite `_SIGNED`, `_FINAL`, `_EXECUTED`, or `_snapshot` artifacts. Use fresh names.

### C. Staging and replacement

1. Copy the already-verified combined artifact to a unique staged app bundle on the same Applications filesystem.
2. Re-run the macOS verifier against that staged copy and compare recorded app.asar/daemon identities.
3. Reconfirm app/ShipIt are stopped and the installed version is the approved baseline.
4. Move the existing app to a unique recoverable backup location only with current-turn approval. Replace with staged candidate; if the move fails, restore the original without deleting it.
5. Do not edit installed `app.asar`, remove quarantine, use ad-hoc signing, or waive Gatekeeper.
6. Start the combined app only after approval for live schema migration. Verify schema 190 and the database-owned revision triggers; no direct SQL mutation of live project configuration.

### D. Routing migration and acceptance

`route_9router.py apply` changes shared profile/workspace settings, Keychain state and project config. It is NOT covered by artifact preparation or installation approval. Request an exact separate current-turn approval before running it.

Before apply:
- Preserve the existing profile path and all MCP entries, specifically Context7 and Foundation. Do not replace `.claude.json` with a fresh file.
- Confirm combined installed API returns numeric project `revision` and committed-row CAS responses.
- Confirm every managed session/reviewer is quiescent under the script's existing admission rules. Idle display alone is insufficient for some TUI/reviewer surfaces.
- Keep full native ownership, registry, credential-file, safe-path, no-follow, no-redirect, redaction and local-key-status gates intact.
- A refusal names missing evidence; do not turn it into a forced write.

After approved apply:
- Run `route_9router.py check` and inspect its scoped verdict.
- Verify project roles remain Claude, fixed local 9router endpoint/profile, `apiKeyHelper` uses the dedicated Keychain credential, and serialized project/settings contain no token.
- Verify Context7/Foundation configuration equality (not just server count); account for legitimate profile auth/cache updates separately.
- Run profile MCP health check; establish Context7 and Foundation connected, then all existing servers.
- Verify stale CAS rejection and malformed-precondition refusal on an isolated disposable project, not by modifying the production project just to test a race.
- Verify a new throwaway task's actual PostCreate context and strict bootstrap ownership through approved isolated project/session tests before enabling future production tasks.
- Verify production inference only with separately approved bounded request; model catalog/local stored-key status do not prove gateway authentication or inference.

### E. UI/data/task acceptance

- Hide routine automation cards; exact loaded count and Show work.
- Switch chats and restart at an approved idle boundary; preference survives.
- Tab focus remains visible on toggle/Show; Show returns focus to conversation log.
- Assistant messages, approvals, errors, recovered notices, protected turn states and real delivery failure surfaces remain visible.
- Stored messages/reports and task identities remain intact; no send/delivery mutation from a display toggle.
- The installed release contract may not expose message-level delivery metadata. Use actual structured turn/activity failures; do not claim direct fixture delivery fields prove transport propagation.
- Existing task controller histories and registered worktrees still bind correctly. Do not resume uncertain PostCreate work without prior-writer-stop evidence.
- Use approved browser/Electron acceptance and screenshots. Avoid clicking user approvals, sending instructions, merging PRs, or triggering routing/infrastructure effects through the UI.

## Rollback

- A rollback is coordinated app AND data recovery, not an unconditional app downgrade.
- Checkpoint/quit again with approval and wait for ShipIt. Preserve failed combined app and post-install DB/logs separately.
- Restoring old app against schema 190 may be unsupported; choose app+pre-migration DB restore only after accounting for every post-cutover message, task, credential/helper/config change.
- Never overwrite newer production data just because the old backup exists. If new work occurred, stop and reconcile.
- Routing apply has no automatic project rollback after a concurrent revision conflict. Do not replace current config with stale backup via unconditional PUT.
- Any explicitly approved routing restore must read current revision, bind CAS to that revision, preserve unrelated concurrent fields, and verify the committed response. If that exact reverse path is not proved, restore nothing automatically.
- Keep Context7/Foundation credentials/settings intact; a backup is not permission to discard updated OAuth tokens blindly.
- Run the macOS verifier on restored app and integrity/FK checks on approved restored DB. Recheck roles/helper/MCPs and task continuity.

## Completion status

Preparation is complete only when one signed/notarized combined artifact and this plan have current verification, full source identity, and review evidence. Production deployment remains a distinct unperformed step until Jason's new cutover and apply approvals. Do not mark the active deployment goal complete on preparation alone.
