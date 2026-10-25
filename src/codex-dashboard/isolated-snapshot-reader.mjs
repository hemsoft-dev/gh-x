import { fork } from "node:child_process";

import { SnapshotBusyError } from "./snapshot-worker.mjs";

// Each slot owns one process and one read. Cancellation frees the slot only
// after process exit, even when a provider ignores signals or blocks its loop.
export function createIsolatedSnapshotReader({ moduleUrl, factoryName, options = {} }) {
    const slots = new Set();
    let closed = false;
    let nextId = 0;

    function stop(slot, error) {
        if (slot.stopping) return;
        slot.stopping = true;
        slot.failure = error;
        // Idle readers are unreferenced. Shutdown must keep the event loop
        // alive until close fires and the caller finishes its own cleanup.
        slot.child.ref();
        slot.child.channel?.ref();
        if (slot.child.exitCode === null && slot.child.signalCode === null) {
            slot.child.kill("SIGKILL");
        }
    }

    function spawnSlot() {
        const child = fork(new URL("./snapshot-reader-process.mjs", import.meta.url), [], {
            windowsHide: true,
            stdio: ["ignore", "ignore", "ignore", "ipc"],
            execArgv: process.execArgv.filter(arg => arg === "--experimental-sqlite"),
        });
        const slot = { child, job: null, stopping: false, failure: null, exited: Promise.withResolvers() };
        slots.add(slot);
        child.on("error", error => stop(slot, error));
        child.on("disconnect", () => stop(slot, new Error("Snapshot reader disconnected.")));
        child.on("message", message => {
            if (slot.stopping || !slot.job || message?.id !== slot.job.id) return;
            const { resolve, reject, cleanup } = slot.job;
            slot.job = null;
            cleanup();
            child.unref();
            child.channel?.unref();
            if (message.error) reject(Object.assign(new Error(message.error.message), { code: message.error.code }));
            else resolve(message.snapshot);
        });
        child.once("close", () => {
            slots.delete(slot);
            if (slot.job) {
                slot.job.cleanup();
                slot.job.reject(slot.failure || new Error("Snapshot reader exited before completing its read."));
                slot.job = null;
            }
            slot.exited.resolve();
        });
        return slot;
    }

    const readSnapshot = (filter, { signal } = {}) => {
        if (closed) return Promise.reject(new Error("Snapshot reader has closed."));
        if (signal?.aborted) return Promise.reject(signal.reason);
        let slot = [...slots].find(candidate => !candidate.job && !candidate.stopping);
        if (!slot) {
            if (slots.size >= 2) return Promise.reject(new SnapshotBusyError());
            slot = spawnSlot();
        }
        const id = ++nextId;
        const result = Promise.withResolvers();
        const onAbort = () => stop(slot, signal.reason);
        slot.job = { id, ...result, cleanup: () => signal?.removeEventListener("abort", onAbort) };
        signal?.addEventListener("abort", onAbort, { once: true });
        slot.child.ref();
        slot.child.channel?.ref();
        try {
            slot.child.send({ id, moduleUrl, factoryName, options, filter }, error => {
                if (error) stop(slot, error);
            });
        } catch (error) {
            stop(slot, error);
        }
        return result.promise;
    };
    readSnapshot.close = async () => {
        closed = true;
        const owned = [...slots];
        for (const slot of owned) stop(slot, new Error("Snapshot reader has closed."));
        await Promise.all(owned.map(slot => slot.exited.promise));
    };
    return readSnapshot;
}
