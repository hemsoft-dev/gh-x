import { parseStatusAcquisition, compareStatusAcquisition, statusBudgets } from "./status.mjs";
import { spawnSync } from "node:child_process";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { compare, parseGo, sampleCount } from "./compare.mjs";

const root = fileURLToPath(new URL("../", import.meta.url));
const args = process.argv.slice(2);
if (args.length && (args.length !== 2 || args[0] !== "--out" || !args[1])) {
    throw new Error("Usage: node benchmarks/run.mjs [--out DIRECTORY]");
}
const output = path.resolve(args[1] ?? path.join(os.tmpdir(), "gh-x-performance", new Date().toISOString().replaceAll(":", "-")));
mkdirSync(output, { recursive: true });
const started = performance.now();
const environment = { ...process.env, GOMAXPROCS: "1", NO_COLOR: "1", TERM: "dumb", GH_X_STATUS_PERF: "1" };
// All child stdio is piped, so Go renders with no TTY width/color dependency.
function run(name, command, argv, timeout = 10_000) {
    const result = spawnSync(command, argv, { cwd: root, env: environment, encoding: "utf8", timeout, maxBuffer: 16 * 1024 * 1024 });
    writeFileSync(path.join(output, `${name}.stdout.txt`), result.stdout ?? "");
    writeFileSync(path.join(output, `${name}.stderr.txt`), result.stderr ?? "");
    if (result.error || result.status !== 0) throw new Error(`${name} failed: ${result.error?.message ?? `exit ${result.status}`}; see ${output}`);
    return result.stdout;
}

try {
    const baseline = JSON.parse(readFileSync(path.join(root, "benchmarks/baseline.json"), "utf8"));
    if (baseline.schemaVersion !== 1 || !baseline.samples) throw new Error("Missing or unsupported baseline");
    const budgets = JSON.parse(readFileSync(path.join(root, "benchmarks/budgets.json"), "utf8"));
    const metadata = {
        recordedAt: new Date().toISOString(), platform: process.platform, arch: process.arch,
        cpu: os.cpus()[0]?.model, logicalCPUs: os.cpus().length, memoryBytes: os.totalmem(), node: process.version,
        go: run("go-version", "go", ["version"]).trim(),
        revision: run("revision", "git", ["rev-parse", "HEAD"]).trim(),
        dirty: run("worktree", "git", ["status", "--porcelain"]).trim(),
        sampleCount, goBenchtime: "150ms", gomaxprocs: 1,
    };
    writeFileSync(path.join(output, "metadata.json"), JSON.stringify(metadata, null, 2));
    writeFileSync(path.join(output, "baseline.json"), JSON.stringify(baseline, null, 2));
    writeFileSync(path.join(output, "budgets.json"), JSON.stringify(budgets, null, 2));
    const raw = run("go", "go", ["test", "./src", "-run", "^$", "-bench", "^BenchmarkCritical$", "-benchmem", "-benchtime=150ms", `-count=${sampleCount}`, "-cpu=1"], 120_000);
    const samples = parseGo(raw);
    for (const size of ["small", "large"]) {
        const result = JSON.parse(run(`node-${size}`, process.execPath, ["--expose-gc", "benchmarks/codex.mjs", size], 60_000));
        Object.assign(samples, result.samples);
    }
    const result = compare(samples, budgets, baseline.samples);
    run("merged-selection", "go", ["test", "./src", "-run", "^(TestFetchRecentlyMergedPullRequestsExpandsUntilMergeOrderIsProven|TestMergedSelectionLargeAndCappedAcquisitions|TestMergedCombinedEvidenceAndPartialFailures|TestMergedSchemaValidationPreservesRows|TestMergedRejectedBatchRecoveryIsBounded)$", "-count=1", "-v"], 120_000);
    const acquisitionRaw = run("status-acquisition", "go", ["test", "./src", "-run", "^TestStatusAcquisitionPerformance$", "-count=1", "-v"], 120_000);
    const acquisitionSamples = parseStatusAcquisition(acquisitionRaw);
    const acquisition = compareStatusAcquisition(acquisitionSamples, statusBudgets(root));
    writeFileSync(path.join(output, "status-acquisition.json"), JSON.stringify({ samples: acquisitionSamples, comparison: acquisition }, null, 2));
    if (!acquisition.passed) throw new Error("Status acquisition budget failed; see status-acquisition.json");
    const report = { acquisition, metadata, elapsedSeconds: (performance.now() - started) / 1000, samples, comparison: result, baseline };
    writeFileSync(path.join(output, "report.json"), JSON.stringify(report, null, 2));
    process.stdout.write(`Performance ${result.passed ? "PASS" : "FAIL"}: ${result.rows.length} metrics in ${report.elapsedSeconds.toFixed(1)}s\nArtifacts: ${output}\n`);
    for (const error of result.errors) process.stderr.write(`${error}\n`);
    if (!result.passed) process.exitCode = 1;
} catch (error) {
    writeFileSync(path.join(output, "error.txt"), error.stack ?? String(error));
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 1;
}
