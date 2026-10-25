# Dashboard background launch contract

The installed dashboard task runs as the installing user, with limited rights
and the `S4U` logon type. Windows schedules S4U tasks on a
[noninteractive desktop](https://learn.microsoft.com/en-us/windows/win32/api/taskschd/ne-taskschd-task_logon_type).
The task uses `-NoProfile -NonInteractive` and always passes `-NoBrowser`.
`-WindowStyle Hidden` is retained as a secondary setting; it is not the
desktop-isolation guarantee.

The installer prepares the native launcher, registers the task and starts it
through Task Scheduler. It never starts the persistent hub as a child of the
interactive installer. An already healthy listener is reused without a process
restart. The loopback service, port, Serve routes and default control/data
locations remain unchanged.

## Console creation and descendants

`scripts/DashboardBackgroundProcess.cs` starts Node directly with
[`CREATE_NO_WINDOW`](https://learn.microsoft.com/en-us/windows/win32/procthread/process-creation-flags).
It passes arguments using Windows CRT quoting, supplies `NUL` for stdin, and
gives Node inherited stdout/stderr file handles. Logging continues after the
short-lived PowerShell launcher exits. A
[`STARTUPINFOEX` handle list](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-updateprocthreadattribute) restricts
inheritance to those files and `NUL`; unrelated launcher pipe writers are not
passed to the persistent child. Otherwise a caller capturing launcher output
could wait for pipe EOF until the persistent hub stops. The native process
handle is acquired
while the child is suspended, before it can exit; startup-timeout cleanup uses
that original handle.

The PowerShell launcher loads a prepared assembly instead of using `Add-Type`
to compile source during background startup. Windows PowerShell 5.1's
[CodeDOM executor](https://github.com/microsoft/referencesource/blob/main/System/compmod/system/codedom/compiler/Executor.cs)
uses a hidden-window request without `CREATE_NO_WINDOW`. Preparation instead
runs the installed .NET Framework compiler through `ProcessStartInfo` with
`UseShellExecute=false`, `CreateNoWindow=true`, closed stdin and captured output.
It has a 30-second deadline. Complete assemblies are published under a name
containing the source SHA256 in the existing control directory. No compiler is
invoked by ordinary background startup; missing preparation fails explicitly.

The hub's owned snapshot-reader forks set `windowsHide: true` with ignored
standard streams and an IPC pipe. In the
[pinned Node/libuv implementation](https://github.com/nodejs/node/blob/v24.21.0/deps/uv/src/win/process.c),
that combination requests `CREATE_NO_WINDOW`. Worker threads do not create
processes. Imported provider extensions can contain their own implementation;
the S4U desktop boundary applies to that descendant chain as well. Source tests
do not certify arbitrary installed extension code or attribute a game crash.

The Go CLI's captured status commands, `gh` API transport, and account-routing
`git config`/`ssh -G` probes also set `CREATE_NO_WINDOW` and `HideWindow` on
Windows. Each independently created console child needs its own flags; an
ancestor's no-console setting is not inherited as a launch policy. Contexts,
pipe handling, deadlines and error/exit semantics are retained. This covers the
owned CLI metadata paths when invoked by background automation. It does not
establish that a particular installed extension invokes them, or identify the
ancestry of an observed window. Explicit editor and review-agent launch paths
remain outside this metadata-launch contract.

## Preparation and containment

Normal installation prepares the assembly automatically. To prepare only,
without starting the hub, registering a task or publishing a route:

```powershell
.\run-dashboard-hub.ps1 -Prepare -NoBrowser
```

`-ControlDirectory <private-directory>` can isolate preparation and qualification
files. Without it, the existing `%USERPROFILE%\.codex\dashboard-hub` location is
used. The default `-NoBrowser` behavior is unchanged; an explicit foreground
launcher invocation can still open its browser when that switch is omitted.

An existing task with `Settings.Enabled=false` or state `Disabled` causes
installation to fail before preparation, process launch, registration or Serve
changes. Rerunning the installer cannot silently undo containment. Task ownership
still requires the same user, executable and exact script path.

S4U stores no password and has documented restrictions on network and
encrypted-file access. These are Windows restrictions, not settings this installer
changes. Before deploying to an existing profile, its owner must verify that
the local dashboard data, extension, executable, logs and configuration are
accessible under that account's S4U context. Do not fall back to an Interactive
principal to resolve an access failure.

## Qualification and deployment gate

The existing offline installer suite executes the actual installer with native
Tailscale doubles and scheduler mocks. It rejects Interactive registration,
inline service launch and attempts to update disabled tasks. It also invokes
the focused launcher qualification:

```powershell
pwsh -NoProfile -File tests/installer/test-dashboard-hub.ps1
pwsh -NoProfile -File tests/installer/test-dashboard-launch.ps1
```

On Windows the focused test prepares the actual native helper, invokes the real
launcher with a private console probe and checks `GetConsoleWindow()==0`,
argument boundaries, working directory, readiness, reuse, log persistence,
startup failures and timeout cleanup. It covers both the PowerShell 7 host and
the existing Windows PowerShell 5.1 CI invocation. It rejects the legacy launch
path before executing it, so a known unsafe version is never launched as a
negative fixture. A real inheritable sentinel pipe detects unrestricted native
handle inheritance without launching an unsafe console path. Captured launcher
pipe EOF has a five-second deadline and must arrive while the ready probe is
still alive; failures retain an owned handle for cleanup. Stage messages identify
preparation, process exit and pipe-drain progress rather than waiting for the
ten-minute CI job limit.
For an offline Windows regression check, `-NativeSourcePath <immutable-source>`
can select the previous no-console helper with unrestricted inheritance. Its
sentinel-pipe assertion must fail before starting the persistent ready probe.
The preflight still requires `CREATE_NO_WINDOW`; this does not execute the old
hidden-only launcher.

The Windows job also re-executes the Go console-subsystem test binary with no
console, then uses the real status and `gh` transports to launch children. Both
parent and children must report `GetConsoleWindow()==0`; the test checks argument
boundaries, stdin, captured streams and nonzero exit diagnostics. Existing
cancellation tests run in that job as well. Linux qualifies source policy,
argument quoting and portable transport behavior only.
Reader tests inspect the actual fork options and then exercise the real child.

These fixtures never register real tasks or read personal dashboard data.
Hosted qualification does not replace deployment proof on the destination.
For [Chief #43](https://github.com/hemsoft-dev/chief/issues/43), merge,
deployment and re-enabling remain root-owned gates. Preserve the backed-up task
XML, task security, script ACLs and logs. Validate the staged source/assembly
hashes, final S4U task definition, account/data compatibility and rollback plan
through authorized noninteractive inspection. Leave contained tasks disabled
until that review is complete. Do not run an interactive task test, open a
browser, sample desktop windows, stop a running dashboard, or touch the game
to validate this change.
