// @vitest-environment node
import { describe, expect, it } from "vitest";
import { SseParser } from "./sse";

const feedAll = (chunks: string[]) => {
  const p = new SseParser();
  return chunks.flatMap((c) => p.feed(c));
};

describe("SseParser", () => {
  const cases: { name: string; chunks: string[]; want: object[] }[] = [
    { name: "one message", chunks: ["id: 1\nevent: a\ndata: x\n\n"], want: [{ id: "1", event: "a", data: "x" }] },
    { name: "split anywhere", chunks: ["id: 1\nev", "ent: a\nda", "ta: x\n", "\n"], want: [{ id: "1", event: "a", data: "x" }] },
    { name: "one byte at a time", chunks: [..."id: 7\ndata: hi\n\n"], want: [{ id: "7", data: "hi" }] },
    { name: "CRLF line endings", chunks: ["id: 1\r\ndata: x\r\n\r\n"], want: [{ id: "1", data: "x" }] },
    { name: "CRLF split between chunks", chunks: ["data: x\r", "\n\r", "\n"], want: [{ data: "x" }] },
    { name: "lone CR line endings", chunks: ["data: x\r\r"], want: [{ data: "x" }] },
    { name: "multi-line data is joined with newlines", chunks: ["data: a\ndata: b\n\n"], want: [{ data: "a\nb" }] },
    { name: "comments are ignored", chunks: [": ping\n\nid: 2\ndata: x\n\n"], want: [{ id: "2", data: "x" }] },
    { name: "a message without data is not dispatched", chunks: ["id: 3\nevent: a\n\n"], want: [] },
    { name: "an event name does not leak into the next message", chunks: ["event: a\n\ndata: x\n\n"], want: [{ data: "x" }] },
    { name: "only one leading space is stripped", chunks: ["data:  two\n\n"], want: [{ data: " two" }] },
    { name: "no space after the colon", chunks: ["data:x\n\n"], want: [{ data: "x" }] },
    { name: "unknown fields are ignored", chunks: ["retry: 5\nfoo: bar\ndata: x\n\n"], want: [{ data: "x" }] },
    { name: "two messages in one chunk", chunks: ["data: a\n\ndata: b\n\n"], want: [{ data: "a" }, { data: "b" }] },
    { name: "an incomplete message waits", chunks: ["data: a\n"], want: [] },
    { name: "an id with a NUL is ignored", chunks: ["id: a\0b\ndata: x\n\n"], want: [{ data: "x" }] },
  ];
  for (const c of cases) {
    it(c.name, () => expect(feedAll(c.chunks)).toEqual(c.want));
  }

  it("keeps the last id for following messages, as the spec says", () => {
    expect(feedAll(["id: 1\ndata: a\n\ndata: b\n\n"])).toEqual([
      { id: "1", data: "a" },
      { id: "1", data: "b" },
    ]);
  });
});
