import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import axe from "axe-core";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createClient } from "../client";
import { ev, fakeServer, type FakeServerOptions } from "../client/testutil";
import { AicliProvider, Conversation, ConversationList } from "./index";

afterEach(cleanup);

function mount(opts: FakeServerOptions = {}, ui?: (id: string) => React.ReactNode) {
  const server = fakeServer(opts);
  const client = createClient({ baseUrl: "/api", fetch: server.fetch, getHeaders: () => ({ Authorization: "Bearer test" }) });
  const view = render(<AicliProvider client={client}>{ui ? ui("c1") : <Conversation conversationId="c1" />}</AicliProvider>);
  return { server, client, ...view };
}

const isDisabled = (el: HTMLElement): boolean => (el as HTMLButtonElement | HTMLTextAreaElement).disabled;
const textbox = () => screen.getByRole("textbox", { name: "Message" });
const sendButton = () => screen.getByRole("button", { name: "Send" });
const open = () => waitFor(() => expect(isDisabled(textbox())).toBe(false));

const history = [
  ev(1, "user.message", { text: "hello there" }),
  ev(2, "turn.started"),
  ev(3, "assistant.text", { text: "Hi! " }),
  ev(4, "assistant.text", { text: "How can I help?" }),
  ev(5, "turn.completed"),
];

describe("<Conversation>", () => {
  it("shows the history the stream replays", async () => {
    mount({ history });
    await screen.findByText("hello there");
    expect(screen.getByText("Hi! How can I help?")).toBeTruthy();
    await open();
  });

  it("renders CLI output as text, never as HTML", async () => {
    const payload = '<img src=x onerror="window.__pwned=1"><script>window.__pwned=1</script>';
    const { container } = mount({ history: [ev(1, "user.message", { text: payload }), ev(2, "assistant.text", { text: payload }), ev(3, "turn.completed")] });
    await waitFor(() => expect(screen.getAllByText(payload)).toHaveLength(2));
    expect(container.querySelector("img, script")).toBeNull();
    expect((window as unknown as { __pwned?: number }).__pwned).toBeUndefined();
  });

  it("keeps Send disabled until the stream is open, and asks for all history first", async () => {
    const { server } = mount();
    expect(isDisabled(textbox())).toBe(true);
    await open();
    expect(server.streams[0]?.lastEventId).toBe("0");
    expect(server.callsTo("/stream")[0]?.headers.get("Authorization")).toBe("Bearer test");
  });

  it("sends a message with an idempotency key, then stays busy until the turn finishes", async () => {
    const user = userEvent.setup();
    const { server } = mount();
    await open();
    await user.type(textbox(), "build it");
    await user.click(sendButton());

    await waitFor(() => expect(server.callsTo("/messages")).toHaveLength(1));
    const body = server.callsTo("/messages")[0]?.body as { message: string; clientMessageId: string };
    expect(body.message).toBe("build it");
    expect(body.clientMessageId).toMatch(/^[A-Za-z0-9_-]{1,64}$/);
    expect((textbox() as HTMLTextAreaElement).value).toBe("");

    // Accepted, but no event yet: still busy.
    expect(isDisabled(textbox())).toBe(true);
    expect(screen.getByRole("status").textContent).toMatch("Working…");

    act(() => {
      server.push(ev(1, "user.message", { turnId: "t-new", text: "build it" }));
      server.push(ev(2, "turn.started", { turnId: "t-new" }));
      server.push(ev(3, "assistant.text", { turnId: "t-new", text: "Done." }));
    });
    await screen.findByText("Done.");
    expect(isDisabled(textbox())).toBe(true); // the turn is still running

    act(() => server.push(ev(4, "turn.completed", { turnId: "t-new" })));
    await waitFor(() => expect(isDisabled(textbox())).toBe(false));
  });

  it("sends with Ctrl+Enter and ignores blank messages", async () => {
    const user = userEvent.setup();
    const { server } = mount();
    await open();
    expect(isDisabled(sendButton())).toBe(true);
    await user.type(textbox(), "   ");
    expect(isDisabled(sendButton())).toBe(true);
    await user.clear(textbox());
    await user.type(textbox(), "go{Control>}{Enter}{/Control}");
    await waitFor(() => expect(server.callsTo("/messages")).toHaveLength(1));
  });

  it("shows an error and keeps the text when sending fails", async () => {
    const user = userEvent.setup();
    mount({ sendError: { status: 409, code: "turn_in_progress", message: "x" } });
    await open();
    await user.type(textbox(), "my long message");
    await user.click(sendButton());
    expect((await screen.findByRole("alert")).textContent).toMatch(/already being processed/i);
    expect((textbox() as HTMLTextAreaElement).value).toBe("my long message");
    expect(isDisabled(textbox())).toBe(false);
  });

  it("cancels a running turn and shows Cancelling until the server confirms", async () => {
    const user = userEvent.setup();
    const { server } = mount({ history: [ev(1, "user.message", { text: "slow" }), ev(2, "turn.started")] });
    await screen.findByText("slow");
    await user.click(await screen.findByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(server.callsTo("/cancel")).toHaveLength(1));
    expect(server.callsTo("/cancel")[0]?.url).toBe("/api/conversations/c1/turns/t1/cancel");
    expect(screen.getByRole("status").textContent).toMatch("Cancelling…");
    expect(screen.queryByRole("button", { name: "Cancel" })).toBeNull();

    act(() => server.push(ev(3, "turn.cancelled")));
    await waitFor(() => expect(isDisabled(textbox())).toBe(false));
    expect(screen.getByText("Cancelled.")).toBeTruthy();
  });

  it("explains a quota failure and lets the user try again", async () => {
    mount({
      history: [ev(1, "user.message", { text: "x" }), ev(2, "turn.failed", { meta: { failure: "quota_exceeded", retry_after_seconds: "7200" } })],
    });
    expect((await screen.findByRole("alert")).textContent).toMatch(/quota.*2 hours/i);
    await open();
  });

  it("turns read-only when the CLI session expired, but history stays visible", async () => {
    mount({ history: [ev(1, "user.message", { text: "old question" }), ev(2, "turn.failed", { meta: { failure: "session_expired" } })] });
    await screen.findByText("old question");
    await waitFor(() => expect(screen.getByText(/can no longer be continued/i, { selector: ".aicli-hint" })).toBeTruthy());
    expect(isDisabled(textbox())).toBe(true);
  });

  it("is read-only from the start for a conversation the server says is not resumable", async () => {
    mount({ info: { resumable: false }, history });
    await screen.findByText("hello there");
    await waitFor(() => expect(screen.getByText(/can no longer be continued/i, { selector: ".aicli-hint" })).toBeTruthy());
    expect(isDisabled(textbox())).toBe(true);
  });

  it("reconnects after the stream drops, resumes from the last event and shows each event once", async () => {
    const { server } = mount({ history: history.slice(0, 3) });
    await screen.findByText("Hi!");
    act(() => server.drop());
    await waitFor(() => expect(server.streams.length).toBeGreaterThanOrEqual(2), { timeout: 5000 });
    expect(server.streams[1]?.lastEventId).toBe("3");
    expect(screen.getAllByText("hello there")).toHaveLength(1);
  });

  it("downloads an export through the authenticated client, using an object URL", async () => {
    const user = userEvent.setup();
    const create = vi.fn(() => "blob:mock");
    const revoke = vi.fn();
    Object.assign(URL, { createObjectURL: create, revokeObjectURL: revoke });
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
      expect(this.download).toBe("conversation-c1.md");
      expect(this.href).toBe("blob:mock");
    });
    const { server } = mount({ history });
    await screen.findByText("hello there");
    await user.click(screen.getByRole("button", { name: "Export Markdown" }));

    await waitFor(() => expect(click).toHaveBeenCalledTimes(1));
    const exportCall = server.callsTo("/export")[0];
    expect(exportCall?.url).toBe("/api/conversations/c1/export?format=md");
    expect(exportCall?.headers.get("Authorization")).toBe("Bearer test"); // sent, unlike a plain link
    expect(create).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(revoke).toHaveBeenCalledWith("blob:mock"));
  });

  it("starts over and closes the old stream when the conversation changes", async () => {
    const server = fakeServer({ history });
    const client = createClient({ baseUrl: "/api", fetch: server.fetch });
    const { rerender } = render(
      <AicliProvider client={client}>
        <Conversation conversationId="c1" />
      </AicliProvider>,
    );
    await screen.findByText("hello there");
    const first = server.streams[0]?.signal;
    rerender(
      <AicliProvider client={client}>
        <Conversation conversationId="c2" />
      </AicliProvider>,
    );
    await waitFor(() => expect(server.streams).toHaveLength(2));
    expect(first?.aborted).toBe(true);
    expect(server.callsTo("/stream").map((c) => c.url)).toEqual(["/api/conversations/c1/stream", "/api/conversations/c2/stream"]);
  });

  it("tells the user when they have no access", async () => {
    const fetchFn = (async () =>
      new Response(JSON.stringify({ error: { code: "not_found", message: "not found" } }), {
        status: 404,
        headers: { "Content-Type": "application/json" },
      })) as unknown as typeof fetch;
    render(
      <AicliProvider client={createClient({ baseUrl: "/api", fetch: fetchFn })}>
        <Conversation conversationId="nope" />
      </AicliProvider>,
    );
    expect((await screen.findByRole("alert")).textContent).toMatch(/does not exist, or you do not have access/i);
    expect(isDisabled(textbox())).toBe(true);
  });

  it("has no accessibility violations", async () => {
    const { container } = mount({ history });
    await screen.findByText("hello there");
    await open();
    const results = await axe.run(container, { rules: { "color-contrast": { enabled: false } } });
    expect(results.violations.map((v) => `${v.id}: ${v.help}`)).toEqual([]);
  });
});

describe("<ConversationList>", () => {
  it("lists conversations, pages, and creates a new one", async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    const { server } = mount({}, () => <ConversationList profile="antigravity" selectedId="c1" onSelect={onSelect} />);
    await screen.findByText("My chat");
    expect(screen.getByText("Second")).toBeTruthy();
    expect(screen.getByRole("button", { name: /My chat/ }).getAttribute("aria-current")).toBe("true");

    await user.click(screen.getByRole("button", { name: /Second/ }));
    expect(onSelect).toHaveBeenLastCalledWith("c2");

    await user.click(screen.getByRole("button", { name: "Load more" }));
    await waitFor(() => expect(server.callsTo("cursor=next")).toHaveLength(1));
    await waitFor(() => expect(screen.queryByRole("button", { name: "Load more" })).toBeNull());
    expect(screen.getByText("Third")).toBeTruthy();
    expect(screen.getByText("Fourth")).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "New conversation" }));
    await waitFor(() => expect(onSelect).toHaveBeenLastCalledWith("c-new"));
    expect(server.callsTo("/conversations").find((c) => c.method === "POST")?.body).toEqual({ profile: "antigravity" });
    expect(within(screen.getByRole("navigation", { name: "Conversations" })).getByText("Untitled")).toBeTruthy();
  });

  it("has no accessibility violations", async () => {
    const { container } = mount({}, () => <ConversationList profile="antigravity" onSelect={() => {}} />);
    await screen.findByText("My chat");
    const results = await axe.run(container, { rules: { "color-contrast": { enabled: false } } });
    expect(results.violations.map((v) => `${v.id}: ${v.help}`)).toEqual([]);
  });
});
