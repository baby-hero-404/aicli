// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { ConversationEvent } from "../client";
import { addEvent, buildTurns, failureMessage, runningTurn, sessionExpired } from "./model";

const ev = (seq: number, kind: ConversationEvent["kind"], extra: Partial<ConversationEvent> = {}): ConversationEvent => ({
  seq,
  kind,
  time: "t",
  turnId: "t1",
  ...extra,
});

describe("addEvent", () => {
  it("appends in order and ignores duplicates and older events, keeping the same array", () => {
    let list: ConversationEvent[] = [];
    list = addEvent(list, ev(1, "user.message"));
    list = addEvent(list, ev(2, "turn.started"));
    const same = list;
    expect(addEvent(list, ev(2, "turn.started"))).toBe(same);
    expect(addEvent(list, ev(1, "user.message"))).toBe(same);
    expect(addEvent(list, ev(3, "turn.completed")).map((e) => e.seq)).toEqual([1, 2, 3]);
  });
});

describe("buildTurns", () => {
  it("builds a finished turn with text deltas joined and tools paired", () => {
    const turns = buildTurns([
      ev(1, "user.message", { text: "read it" }),
      ev(2, "turn.started"),
      ev(3, "tool.use", { tool: { name: "read_file", summary: '{"Path":"a"}' } }),
      ev(4, "tool.result", { tool: { name: "read_file", summary: "1 lines" } }),
      ev(5, "assistant.text", { text: "It says " }),
      ev(6, "assistant.text", { text: "hi.\n" }),
      ev(7, "turn.completed"),
    ]);
    expect(turns).toEqual([
      {
        id: "t1",
        user: "read it",
        assistant: "It says hi.\n",
        tools: [{ name: "read_file", summary: "1 lines", done: true }],
        status: "completed",
      },
    ]);
  });

  it("keeps a running turn running and exposes it", () => {
    const turns = buildTurns([ev(1, "user.message", { text: "x" }), ev(2, "turn.started"), ev(3, "assistant.text", { text: "par" })]);
    expect(runningTurn(turns)?.assistant).toBe("par");
  });

  it("separates turns, and skips events that belong to none", () => {
    const turns = buildTurns([
      ev(1, "user.message", { text: "a", turnId: "t1" }),
      ev(2, "turn.completed", { turnId: "t1" }),
      { seq: 3, kind: "assistant.text", time: "t", text: "orphan" },
      ev(4, "user.message", { text: "b", turnId: "t2" }),
    ]);
    expect(turns.map((t) => [t.id, t.status])).toEqual([["t1", "completed"], ["t2", "running"]]);
  });

  it("reads the failure code and retry hint", () => {
    const [t] = buildTurns([
      ev(1, "user.message", { text: "x" }),
      ev(2, "turn.failed", { meta: { failure: "quota_exceeded", retry_after_seconds: "7200" } }),
    ]);
    expect(t).toMatchObject({ status: "failed", failure: "quota_exceeded", retryAfterSeconds: 7200 });
  });

  it("marks a tool result error and pairs results with the latest open call of that name", () => {
    const [t] = buildTurns([
      ev(1, "tool.use", { tool: { name: "run" } }),
      ev(2, "tool.use", { tool: { name: "run", summary: "second" } }),
      ev(3, "tool.result", { tool: { name: "run", summary: "boom", isError: true } }),
    ]);
    expect(t?.tools).toEqual([
      { name: "run", done: false },
      { name: "run", summary: "boom", done: true, isError: true },
    ]);
  });

  it("detects an expired session", () => {
    const turns = buildTurns([ev(1, "turn.failed", { meta: { failure: "session_expired" } })]);
    expect(sessionExpired(turns)).toBe(true);
    expect(sessionExpired(buildTurns([ev(1, "turn.completed")]))).toBe(false);
  });
});

describe("failureMessage", () => {
  it.each([
    ["quota_exceeded", undefined, /quota/i],
    ["quota_exceeded", 7200, /2 hours/],
    ["quota_exceeded", 600, /10 minutes/],
    ["quota_exceeded", 30, /30 seconds/],
    ["auth_invalid", undefined, /sign in/i],
    ["awaiting_input", undefined, /question/i],
    ["session_expired", undefined, /no longer be continued/i],
    ["interrupted", undefined, /interrupted/i],
    ["run_failed", undefined, /failed/i],
    [undefined, undefined, /failed/i],
  ])("%s %s", (code, secs, pattern) => {
    expect(failureMessage(code, secs)).toMatch(pattern);
  });
});
