// Records a real team run in the Office tab for the README (Phase 11, docs/demo.md).
//
// Not a test: it starts a real daemon with sandboxed Claude Code agents (costs a few cents of
// haiku tokens), on a throwaway clone of AO_DEMO_REPO, with a scratch data dir, never the real
// one. The browser is paired like a user's (`agent-office pair` prints a #pair= link), the goal is
// typed into the board, the Tech Lead is started with its button, and the Office tab is recorded
// until the team has reported the goal. Output in AO_DEMO_OUT: team-run.webm, a few PNG stills
// and markers.json (seconds into the video at each step), for demo/to-gif.sh.
//
//   make demo AO_DEMO_REPO=~/projects/agent-office-playground
import { chromium, type Page } from "@playwright/test";
import { execFileSync, spawn } from "node:child_process";
import { mkdirSync, mkdtempSync, openSync, readdirSync, renameSync, rmSync, writeFileSync } from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

const env = (name: string, fallback?: string): string => {
  const v = process.env[name] ?? fallback;
  if (v === undefined || v === "") {
    throw new Error(`set ${name}`);
  }
  return v;
};

const sourceRepo = resolve(env("AO_DEMO_REPO"));
const bin = resolve(env("AO_BIN", "../bin/agent-office"));
const model = env("AO_DEMO_MODEL", "haiku");
const pack = env("AO_DEMO_PACK", "go");
const timeoutMin = Number(env("AO_DEMO_TIMEOUT_MIN", "20"));
const keep = process.env.AO_DEMO_KEEP === "1";
// The clone's folder name is the project name the UI shows.
const work = mkdtempSync(join(process.env.AO_DEMO_WORK ?? tmpdir(), "ao-demo-"));
const out = resolve(env("AO_DEMO_OUT", join(work, "out")));
mkdirSync(out, { recursive: true });
const repo = join(work, "linkshelf");
const dataDir = join(work, "data");

// The two sub-tasks touch different files, so the Tech Lead can run them in parallel (file
// ownership, ADR 0032): one in the HTTP package, one in the bookmark store.
const goal = {
  title: "Health check and duplicate URLs",
  description:
    "Split this goal into exactly two sub-tasks and assign both of them right away; they touch different " +
    "files, so they run in parallel:\n" +
    "1. Add GET /healthz, answering 200 with {\"status\":\"ok\"}, with a test. Files: " +
    "internal/httpapi/handler.go and internal/httpapi/handler_test.go only.\n" +
    "2. Make bookmark.Store.Create reject a URL that is already saved, with an error that wraps ErrInvalid " +
    "(the HTTP API already answers 422 for it), with a test. Files: internal/bookmark/store.go and " +
    "internal/bookmark/store_test.go only.\n" +
    "Give each sub-task exactly those files. Each sub-task description must say what to change and that " +
    "`go test ./...` must pass; the worker commits its change and then calls report_done. When both workers " +
    "have reported, call report_done for the goal.",
};

interface Agent { id: string; state: string; kind: string; task_id?: string }
interface Task { id: string; title: string; status: string; parent_task_id?: string }

const started = Date.now();
const markers: Record<string, number> = {};
const mark = (name: string) => {
  markers[name] = (Date.now() - started) / 1000;
  console.log(`[${markers[name].toFixed(0).padStart(4)}s] ${name}`);
};

function freePort(): Promise<number> {
  return new Promise((ok, fail) => {
    const s = createServer();
    s.listen(0, "127.0.0.1", () => {
      const addr = s.address();
      s.close(() => (addr && typeof addr === "object" ? ok(addr.port) : fail(new Error("no port"))));
    });
  });
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

async function main() {
  execFileSync("git", ["clone", "-q", "--no-hardlinks", sourceRepo, repo]);
  const port = await freePort();
  const base = `http://127.0.0.1:${port}`;
  const log = openSync(join(work, "daemon.log"), "w");
  const daemon = spawn(bin, ["serve", "--repo", repo, "--addr", `127.0.0.1:${port}`, "--data-dir", dataDir,
    "--model", model, "--sandbox", "podman", "--pack", pack, "--qa=false", "--max-agents", "3"],
  { stdio: ["ignore", log, log] });
  let token = "";
  const api = async <T>(method: string, path: string, body?: unknown): Promise<T> => {
    const r = await fetch(base + path, {
      method, headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    if (!r.ok) {
      throw new Error(`${method} ${path}: ${r.status} ${await r.text()}`);
    }
    return (await r.json()) as T;
  };

  const browser = await chromium.launch({ headless: true });
  const videoDir = join(work, "video");
  const context = await browser.newContext({
    viewport: { width: 1280, height: 800 }, deviceScaleFactor: 1,
    recordVideo: { dir: videoDir, size: { width: 1280, height: 800 } },
    serviceWorkers: "block", colorScheme: "dark",
  });
  const page = await context.newPage();
  try {
    for (let i = 0; ; i++) {
      try {
        if ((await fetch(base + "/api/health")).ok) break;
      } catch { /* not up yet */ }
      if (i > 100) throw new Error("daemon did not start; see " + join(work, "daemon.log"));
      await sleep(200);
    }
    const pair = (...args: string[]) => execFileSync(bin, ["pair", "--data-dir", dataDir, ...args],
      { encoding: "utf8", stdio: ["ignore", "pipe", "ignore"] });
    token = pair("--token", "demo-recorder").trim();
    const link = /#pair=([A-Z0-9-]+)/.exec(pair())?.[1];
    if (!link) throw new Error("no pairing code");

    await page.goto(`${base}/#pair=${link}`);
    await page.getByRole("button", { name: "Pair" }).click();
    await page.getByRole("button", { name: "+ New task" }).waitFor();
    mark("paired");
    await sleep(1500);

    await page.getByRole("button", { name: "+ New task" }).click();
    await page.getByPlaceholder("Title").fill(goal.title);
    await sleep(600);
    await page.getByPlaceholder("Description (optional)").fill(goal.description);
    await sleep(1200);
    await page.getByRole("button", { name: "Add", exact: true }).click();
    const card = page.locator("article.task").filter({ hasText: goal.title });
    await card.getByRole("button", { name: "Start Tech Lead" }).click();
    mark("lead started");
    await sleep(3000);

    await tab(page, "Office");
    mark("office");
    const deadline = Date.now() + timeoutMin * 60_000;
    let stills = 0;
    for (;;) {
      if (Date.now() > deadline) throw new Error(`the team did not report within ${timeoutMin} minutes`);
      const [agents, tasks] = await Promise.all([api<Agent[]>("GET", "/api/agents"), api<Task[]>("GET", "/api/tasks")]);
      const g = tasks.find((t) => t.title === goal.title && !t.parent_task_id);
      const working = agents.filter((a) => a.kind === "worker" && a.state === "working").length;
      if (stills === 0 && working >= 2) {
        await sleep(2000);
        await page.screenshot({ path: join(out, "office-team-working.png") });
        stills++;
        mark("two workers working");
      }
      // Office v2 (ADR 0045): a worker that reports walks to the Tech Lead's whiteboard.
      const reported = tasks.some((t) => t.parent_task_id === g?.id && (t.status === "review" || t.status === "done"));
      if (stills === 1 && reported) {
        await sleep(2500);
        await page.screenshot({ path: join(out, "office-worker-reporting.png") });
        stills++;
        mark("first report");
      }
      const lead = agents.find((a) => a.kind === "lead");
      if (g?.status === "review" && lead?.state === "idle") break;
      await sleep(2000);
    }
    mark("goal reported");
    // The Tech Lead reports at its whiteboard; the last workers walk out.
    await sleep(8000);
    await page.screenshot({ path: join(out, "office-goal-reported.png") });
    await tab(page, "Board");
    await sleep(4000);
    await page.screenshot({ path: join(out, "board-goal-reported.png") });
    await tab(page, "Usage");
    await sleep(4000);
    await page.screenshot({ path: join(out, "usage-team-run.png") });
    mark("end");
  } finally {
    const video = page.video();
    await context.close();
    await browser.close();
    const webm = await video?.path();
    if (webm) renameSync(webm, join(out, "team-run.webm"));
    writeFileSync(join(out, "markers.json"), JSON.stringify(markers, null, 2) + "\n");
    await cleanup(api, daemon);
  }
  console.log(`output: ${out} (${readdirSync(out).join(", ")})`);
}

async function tab(page: Page, name: string) {
  await page.locator("nav.tabs").getByRole("button", { name, exact: true }).click();
}

async function cleanup(api: <T>(m: string, p: string, b?: unknown) => Promise<T>, daemon: ReturnType<typeof spawn>) {
  let ids: string[] = [];
  try {
    ids = (await api<Agent[]>("GET", "/api/agents")).map((a) => a.id);
    for (const id of ids) {
      await api("POST", `/api/agents/${id}/stop`).catch(() => undefined);
    }
  } catch { /* the daemon may already be gone */ }
  daemon.kill("SIGTERM");
  await new Promise((r) => (daemon.exitCode !== null ? r(null) : daemon.once("exit", r)));
  for (const id of ids) {
    try { execFileSync("podman", ["rm", "-f", `ao-${id}`], { stdio: "ignore" }); } catch { /* already gone */ }
  }
  if (!keep) {
    // Sandboxed workspaces belong to a subordinate uid (ADR 0014).
    try { execFileSync("podman", ["unshare", "rm", "-rf", dataDir], { stdio: "ignore" }); } catch { /* best effort */ }
    rmSync(repo, { recursive: true, force: true });
  }
}

main().catch((err: unknown) => {
  console.error(err instanceof Error ? err.message : err);
  process.exit(1);
});
