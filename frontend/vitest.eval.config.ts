import { defineConfig } from "vitest/config";
import base from "./vitest.config.ts";

/**
 * The agent evals: `just agent-eval`. They run a real Vantage server and a real
 * headless Claude Code session, so they need the network, spend money and take
 * minutes, and their results vary from run to run. That is why they live outside
 * `src/`, where the gate's vitest run never looks.
 *
 * They run without the unit tests' setup file, which stops every request from
 * leaving the process: these talk to a real server. They keep jsdom, because the
 * review store reads browser state when it loads.
 * The fields are replaced rather than merged, since mergeConfig would append
 * this include to the unit tests' one.
 */
export default defineConfig({
  ...base,
  test: {
    ...base.test,
    include: ["evals/**/*.eval.ts"],
    setupFiles: [],
    environment: "jsdom",
    fileParallelism: false,
    testTimeout: 15 * 60_000,
    hookTimeout: 5 * 60_000,
  },
});
