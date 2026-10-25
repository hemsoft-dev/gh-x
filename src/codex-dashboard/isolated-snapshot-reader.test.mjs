import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import childProcess from "node:child_process";
import { syncBuiltinESMExports } from "node:module";
import { once } from "node:events";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { setTimeout as delay } from "node:timers/promises";
import test from "node:test";

import { createIsolatedSnapshotReader } from "./isolated-snapshot-reader.mjs";
import { hasRunningProcessState, isProcessRunning, readProcessStartTime } from "./test-process-liveness.mjs";
import { createSnapshotWorker, SnapshotBusyError } from "./snapshot-worker.mjs";
import { createDashboardServer } from "./dashboard-server.mjs";
import { createDashboardHub } from "../dashboard-hub/hub-server.mjs";

test("isolated reader forks prohibit Windows console creation", async t => {
    const original = childProcess.fork;
    let calls = 0;
    t.mock.method(childProcess, "fork", (entry, args, options) => {
        calls++;
        assert.equal(options.windowsHide, true);
        assert.deepEqual(options.stdio, ["ignore", "ignore", "ignore", "ipc"]);
        return original(entry, args, options);
    });
    syncBuiltinESMExports();
    t.after(() => { t.mock.restoreAll(); syncBuiltinESMExports(); });
    const { reader } = await fixture(t);
    assert.equal((await reader({})).available, true);
    assert.equal(calls, 1);
});

test("process liveness treats Linux zombies and dead states as terminated", async () => {
    assert.equal(hasRunningProcessState("123 (node worker) R 1 0"), true);
    assert.equal(hasRunningProcessState("123 (node (worker)) Z 1 0"), false);
    assert.equal(hasRunningProcessState("123 (node) X 1 0"), false);
    assert.throws(() => hasRunningProcessState("invalid"), /process state/);
    const stat = "123 (node worker) R " + Array(18).fill("0").join(" ") + " 1234";
    assert.equal(hasRunningProcessState(stat, "1234"), true);
    assert.equal(hasRunningProcessState(stat, "5678"), false, "A recycled PID is not the original reader");
    assert.equal(await isProcessRunning(process.pid, await readProcessStartTime(process.pid)), true);
});

async function fixture(t) {
    const directory = await mkdtemp(path.join(os.tmpdir(), "snapshot-cancellation-"));
    const modulePath = path.join(directory, "adapter.mjs");
    await writeFile(modulePath, `
        import { writeFileSync, renameSync } from "node:fs";
        import { isMainThread } from "node:worker_threads";
        import { readProcessStartTime } from ${JSON.stringify(new URL("./test-process-liveness.mjs", import.meta.url).href)};
        export function createAdapter({ directory }) {
            let reads = 0;
            return { async getSnapshot(filter) {
                const processStartTime = await readProcessStartTime(process.pid);
                reads++;
                if ([3600000, 86400000].includes(filter.recentWindowMs)) {
                    const marker = directory + "/" + filter.recentWindowMs + ".json";
                    writeFileSync(marker + ".tmp", JSON.stringify({ pid: process.pid, processStartTime, computationOnMainThread: isMainThread }));
                    renameSync(marker + ".tmp", marker);
                    // A blocked loop cannot observe AbortSignal or IPC messages.
                    while (true) {}
                }
                if (filter.recentWindowMs === 259200000) process.exit(7);
                if (filter.recentWindowMs === 604800000) throw Object.assign(new Error("fixture failure"), { code: "fixture_error" });
                return { available: true, filter, pid: process.pid, reads };
            }};
        }
    `);
    const reader = createIsolatedSnapshotReader({
        moduleUrl: pathToFileURL(modulePath).href, factoryName: "createAdapter", options: { directory },
    });
    t.after(async () => { await reader.close(); await rm(directory, { recursive: true, force: true }); });
    return { reader, directory };
}

async function started(directory, filter) {
    const deadline = Date.now() + 5_000;
    while (Date.now() < deadline) {
        try { return JSON.parse(await readFile(path.join(directory, `${filter}.json`), "utf8")); }
        catch (error) { if (error.code !== "ENOENT") throw error; }
        await delay(10);
    }
    throw new Error("Fixture read did not start.");
}

function assertExited(pid) {
    assert.throws(() => process.kill(pid, 0), error => error.code === "ESRCH", `reader ${pid} must have exited`);
}

test("isolated reads reuse a warm adapter and propagate provider errors", async t => {
    const { reader } = await fixture(t);
    const first = await reader({ recentWindowMs: 21_600_000 });
    const next = await reader({ recentWindowMs: 43_200_000 });
    assert.equal(first.pid, next.pid);
    assert.equal(next.reads, 2);
    assert.deepEqual(next.filter, { recentWindowMs: 43_200_000 });
    await assert.rejects(reader({ recentWindowMs: 604_800_000 }), error => error.code === "fixture_error");
    assert.equal((await reader({})).reads, 4);
});

test("isolated reads kill two non-cooperative computations before releasing capacity", { timeout: 10_000 }, async t => {
    const { reader, directory } = await fixture(t);
    const controllers = [new AbortController(), new AbortController()];
    const reads = [3_600_000, 86_400_000].map((recentWindowMs, i) => reader({ recentWindowMs }, {
        signal: controllers[i].signal,
    }).catch(error => error));
    const pids = await Promise.all([3_600_000, 86_400_000].map(filter => started(directory, filter)));
    await assert.rejects(reader({}), SnapshotBusyError);
    controllers.forEach(controller => controller.abort(new Error("fixture abandoned")));
    // Synchronous abort does not prematurely free slots while processes still live.
    await assert.rejects(reader({}), SnapshotBusyError);
    assert.deepEqual((await Promise.all(reads)).map(error => error.message), ["fixture abandoned", "fixture abandoned"]);
    pids.forEach(({ pid }) => assertExited(pid));
    assert.deepEqual((await reader({ recentWindowMs: 21_600_000 })).filter, { recentWindowMs: 21_600_000 });
});

test("isolated Codex adapter reads a synthetic SQLite store and preserves filter semantics", async t => {
    const directory = await mkdtemp(path.join(os.tmpdir(), "codex-isolated-store-"));
    const { DatabaseSync } = await import("node:sqlite");
    const database = new DatabaseSync(path.join(directory, "state_5.sqlite"));
    database.exec(`CREATE TABLE threads (
        id TEXT, rollout_path TEXT, created_at_ms INTEGER, updated_at_ms INTEGER,
        cwd TEXT, title TEXT, tokens_used INTEGER, model TEXT, git_branch TEXT, archived INTEGER
    )`);
    const rollout = path.join(directory, "rollout.jsonl");
    await writeFile(rollout, "");
    const now = Date.now();
    database.prepare("INSERT INTO threads VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)").run(
        "synthetic-session", rollout, now - 60_000, now, directory, "Synthetic session", 200_000, "fixture-model", "fixture-branch", 0,
    );
    database.close();
    const reader = createIsolatedSnapshotReader({
        moduleUrl: new URL("./codex-adapter.mjs", import.meta.url).href,
        factoryName: "createCodexAdapter", options: { codexHome: directory },
    });
    t.after(async () => { await reader.close(); await rm(directory, { recursive: true, force: true }); });
    const snapshot = await reader({ recentWindowMs: 3_600_000 });
    assert.equal(snapshot.available, true);
    assert.equal(snapshot.totals.totalTokens, 200_000);
    assert.equal(snapshot.windows.recentMs, 3_600_000);
    assert.equal(snapshot.sessions[0].sessionId, "synthetic-session");
});

test("IPC serialization failure retires its process instead of leaking a slot", async t => {
    const { reader } = await fixture(t);
    const invalid = {}; invalid.self = invalid;
    await assert.rejects(reader(invalid), /circular/i);
    assert.equal((await reader({})).available, true);
});

test("abrupt parent exit stops a reader whose computation blocks its event loop", { timeout: 10_000 }, async t => {
    let child;
    let childClosed;
    let readerPid;
    let readerStartTime;
    let markerPath;
    let readerExited = false;
    let observedExitedPid;
    const originalKill = process.kill;
    process.kill = (pid, signal) => {
        if (pid === observedExitedPid && signal === "SIGKILL") throw new Error("Refusing to signal an exited reader PID.");
        return originalKill(pid, signal);
    };
    t.after(async () => {
        try {
            if (child && child.exitCode === null && child.signalCode === null) child.kill("SIGKILL");
            await childClosed?.catch(() => {});
            if (readerExited) return;
            if (!readerPid && markerPath) {
                try {
                    const marker = JSON.parse(await readFile(markerPath, "utf8"));
                    readerPid = marker.pid;
                    readerStartTime = marker.processStartTime;
                }
                catch (error) { if (error.code !== "ENOENT") throw error; }
            }
            if (readerPid && await isProcessRunning(readerPid, readerStartTime)) {
                try { process.kill(readerPid, "SIGKILL"); } catch (error) { if (error.code !== "ESRCH") throw error; }
                const deadline = Date.now() + 2_000;
                while (Date.now() < deadline) {
                    if (!await isProcessRunning(readerPid, readerStartTime)) return;
                    await delay(10);
                }
                assert.fail("Test-owned blocked reader did not terminate during cleanup.");
            }
        } finally { process.kill = originalKill; }
    });
    const { directory } = await fixture(t);
    markerPath = path.join(directory, "3600000.json");
    const driver = path.join(directory, "abrupt-parent.mjs");
    await writeFile(driver, `
        import { readFile } from "node:fs/promises";
        import { setTimeout as delay } from "node:timers/promises";
        import { createIsolatedSnapshotReader } from ${JSON.stringify(new URL("./isolated-snapshot-reader.mjs", import.meta.url).href)};
        const directory = ${JSON.stringify(directory)};
        const reader = createIsolatedSnapshotReader({
            moduleUrl: ${JSON.stringify(pathToFileURL(path.join(directory, "adapter.mjs")).href)},
            factoryName: "createAdapter", options: { directory },
        });
        reader({ recentWindowMs: 3600000 }).catch(() => {});
        while (true) {
            try { await readFile(directory + "/3600000.json"); break; }
            catch (error) { if (error.code !== "ENOENT") throw error; await delay(10); }
        }
        process.exit(42);
    `);
    child = spawn(process.execPath, [driver], { stdio: ["ignore", "ignore", "ignore"] });
    childClosed = once(child, "close");
    childClosed.catch(() => {});
    const [code] = await childClosed;
    assert.equal(code, 42);
    const blocked = await started(directory, 3_600_000);
    readerPid = blocked.pid;
    readerStartTime = blocked.processStartTime;
    assert.equal(blocked.computationOnMainThread, false, "Blocked computation must not share the IPC control event loop");
    const deadline = Date.now() + 1_000;
    let alive = true;
    do {
        alive = await isProcessRunning(readerPid, readerStartTime);
        if (alive) await delay(10);
    } while (alive && Date.now() < deadline);
    assert.equal(alive, false, "Blocked reader survived abrupt parent exit");
    readerExited = true;
    observedExitedPid = readerPid;
    readerPid = undefined;
});

test("idle shutdown keeps the process alive until reader cleanup completes", { timeout: 10_000 }, async t => {
    let child;
    let childClosed;
    t.after(async () => {
        if (child && child.exitCode === null && child.signalCode === null) child.kill("SIGKILL");
        await childClosed?.catch(() => {});
    });
    const { directory } = await fixture(t);
    const driver = path.join(directory, "idle-shutdown.mjs");
    const marker = path.join(directory, "shutdown-completed.json");
    await writeFile(driver, `
        import { writeFile } from "node:fs/promises";
        import { createIsolatedSnapshotReader } from ${JSON.stringify(new URL("./isolated-snapshot-reader.mjs", import.meta.url).href)};
        const reader = createIsolatedSnapshotReader({
            moduleUrl: ${JSON.stringify(pathToFileURL(path.join(directory, "adapter.mjs")).href)},
            factoryName: "createAdapter", options: { directory: ${JSON.stringify(directory)} },
        });
        const snapshot = await reader({});
        await reader.close();
        await writeFile(${JSON.stringify(marker)}, JSON.stringify({ pid: snapshot.pid, closed: true }));
    `);
    child = spawn(process.execPath, [driver], { stdio: ["ignore", "ignore", "pipe"] });
    let output = "";
    child.stderr.on("data", chunk => { output += chunk; });
    childClosed = once(child, "close");
    childClosed.catch(() => {});
    const [code] = await childClosed;
    assert.equal(code, 0, output);
    const completed = JSON.parse(await readFile(marker, "utf8"));
    assert.equal(completed.closed, true);
    assertExited(completed.pid);
});

test("cancellation during reader startup releases capacity after exit", async t => {
    const { reader } = await fixture(t);
    const controller = new AbortController();
    const pending = reader({}, { signal: controller.signal }).catch(error => error);
    controller.abort(new Error("cancel startup"));
    assert.equal((await pending).message, "cancel startup");
    assert.equal((await reader({})).available, true);
});

test("pre-aborted and closed readers do not start work", async t => {
    const { reader } = await fixture(t);
    const controller = new AbortController();
    controller.abort(new Error("already cancelled"));
    await assert.rejects(reader({}, { signal: controller.signal }), /already cancelled/);
    await reader.close();
    await reader.close();
    await assert.rejects(reader({}), /closed/);
});

test("unexpected reader exit releases its slot and a later read recovers", async t => {
    const { reader } = await fixture(t);
    await assert.rejects(reader({ recentWindowMs: 259_200_000 }), /exited|disconnected/);
    assert.equal((await reader({})).available, true);
});

test("worker shutdown stops active and idle processes and rejects later reads", async t => {
    const { reader, directory } = await fixture(t);
    const worker = createSnapshotWorker(reader);
    const busy = worker({ recentWindowMs: 3_600_000 }).catch(error => error);
    const active = await started(directory, 3_600_000);
    const idle = await worker({});
    assert.notEqual(active.pid, idle.pid);
    await worker.close();
    await worker.close();
    assert.match((await busy).message, /stopped/);
    assertExited(active.pid);
    assertExited(idle.pid);
    await assert.rejects(worker({}), /closed/);
});

test("opaque installed Copilot reads are terminated without changing the extension", { timeout: 10_000 }, async t => {
    const directory = await mkdtemp(path.join(os.tmpdir(), "copilot-isolated-fixture-"));
    const modules = {
        "session-store.mjs": `import { writeFileSync, renameSync } from "node:fs";
            export async function readSessionStoreSnapshot(filter) {
                if ([3600000, 86400000].includes(filter.recentWindowMs)) {
                    const marker = ${JSON.stringify(directory)} + "/" + filter.recentWindowMs + ".json";
                    writeFileSync(marker + ".tmp", JSON.stringify({ pid: process.pid }));
                    renameSync(marker + ".tmp", marker);
                    await new Promise(() => {});
                }
                return { available: true, filter: { recentWindowMs: filter.recentWindowMs }, billingPeriod: {} };
            }`,
        "session-activity.mjs": "export const createSessionActivityRegistry = () => ({ overlay: async snapshot => snapshot });",
        "plan-usage.mjs": "export const buildPlanUsage = () => ({ available: true });",
        "account-quota-cache.mjs": "export const createAccountQuotaCache = () => ({ read: async () => ({}) });",
    };
    await Promise.all(Object.entries(modules).map(([name, source]) => writeFile(path.join(directory, name), source)));
    const reader = createIsolatedSnapshotReader({
        moduleUrl: new URL("../dashboard-hub/copilot-adapter.mjs", import.meta.url).href,
        factoryName: "createCopilotAdapter", options: { extensionDirectory: directory },
    });
    t.after(async () => { await reader.close(); await rm(directory, { recursive: true, force: true }); });
    const controllers = [new AbortController(), new AbortController()];
    const reads = [3_600_000, 86_400_000].map((recentWindowMs, i) => reader({ recentWindowMs }, {
        signal: controllers[i].signal,
    }).catch(error => error));
    const pids = await Promise.all([3_600_000, 86_400_000].map(filter => started(directory, filter)));
    controllers.forEach(controller => controller.abort(new Error("cancel Copilot")));
    await Promise.all(reads);
    pids.forEach(({ pid }) => assertExited(pid));
    const recovered = await reader({ recentWindowMs: 21_600_000 });
    assert.equal(recovered.available, true);
    assert.deepEqual(recovered.filter, { recentWindowMs: 21_600_000 });
});

for (const mode of ["standalone", "hub"]) {
    test(`${mode} aborts blocked processes on disconnect and recovers the current filter`, { timeout: 10_000 }, async t => {
        const { reader, directory } = await fixture(t);
        const server = mode === "standalone"
            ? await createDashboardServer({ token: "isolated-fixture", getSnapshot: reader })
            : await createDashboardHub({
                codexAdapter: { getSnapshot: reader },
                copilotAdapter: { extensionDirectory: "fixture", getSnapshot: async () => ({ available: true, provider: "copilot" }) },
                readFileImpl: async () => "<html><head></head><body>fixture</body></html>", port: 0,
            });
        t.after(() => server.close());
        const api = mode === "standalone" ? `${server.url}api/usage` : `${server.url}dashboards/codex/api/usage`;
        const controllers = [new AbortController(), new AbortController()];
        const requests = [3_600_000, 86_400_000].map((recentWindowMs, i) => fetch(`${api}?recentWindowMs=${recentWindowMs}`, {
            signal: controllers[i].signal,
        }).catch(error => error));
        const pids = await Promise.all([3_600_000, 86_400_000].map(filter => started(directory, filter)));
        controllers.forEach(controller => controller.abort());
        await Promise.all(requests);
        const deadline = Date.now() + 3_000;
        let recovered;
        do {
            recovered = await fetch(`${api}?recentWindowMs=21600000`);
            if (recovered.status === 503) { await recovered.arrayBuffer(); await delay(10); }
        } while (recovered.status === 503 && Date.now() < deadline);
        assert.equal(recovered.status, 200);
        assert.deepEqual((await recovered.json()).filter, { recentWindowMs: 21_600_000 });
        pids.forEach(({ pid }) => assertExited(pid));
        assert.equal((await fetch(`${server.url}api/health`)).status, 200);
        if (mode === "hub") assert.equal((await (await fetch(`${server.url}copilot/api/usage`)).json()).provider, "copilot");
    });

    test(`${mode} read deadline stops the process even when the client remains connected`, { timeout: 10_000 }, async t => {
        const { reader, directory } = await fixture(t);
        const server = mode === "standalone"
            ? await createDashboardServer({ token: "deadline-fixture", getSnapshot: reader, snapshotTimeoutMs: 1_000 })
            : await createDashboardHub({
                codexAdapter: { getSnapshot: reader }, copilotAdapter: { extensionDirectory: "fixture", getSnapshot: async () => ({ available: true }) },
                readFileImpl: async () => "<html><head></head><body>fixture</body></html>", port: 0, snapshotTimeoutMs: 1_000,
            });
        t.after(() => server.close());
        const api = mode === "standalone" ? `${server.url}api/usage` : `${server.url}codex/api/usage`;
        const pending = fetch(`${api}?recentWindowMs=3600000`);
        const { pid } = await started(directory, 3_600_000);
        const failed = await pending;
        assert.equal(failed.status, 500);
        assert.equal((await failed.json()).diagnostics.code, "dashboard_snapshot_timeout");
        assertExited(pid);
        const recovered = await fetch(`${api}?recentWindowMs=21600000`);
        assert.equal(recovered.status, 200);
        assert.deepEqual((await recovered.json()).filter, { recentWindowMs: 21_600_000 });
    });
}
