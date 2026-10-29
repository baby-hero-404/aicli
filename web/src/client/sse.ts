/** One message of a text/event-stream response. */
export interface SseMessage {
  id?: string;
  event?: string;
  data: string;
}

/**
 * Incremental parser for text/event-stream. Feed it text as it arrives, in chunks of any size;
 * it returns the messages completed by that chunk. Comment lines and unknown fields are
 * ignored; a message without data is not dispatched, as the spec requires.
 */
export class SseParser {
  #buffer = "";
  #id: string | undefined;
  #event: string | undefined;
  #data: string[] = [];
  #sawCR = false;

  feed(chunk: string): SseMessage[] {
    const out: SseMessage[] = [];
    // A CR ends a line at once; if its LF arrives at the start of the next chunk, drop it, so a
    // CRLF split across chunks does not count as two line endings.
    if (this.#sawCR && chunk.startsWith("\n")) chunk = chunk.slice(1);
    this.#sawCR = chunk.endsWith("\r");
    this.#buffer += chunk;
    for (;;) {
      const m = /\r\n|\n|\r/.exec(this.#buffer);
      if (!m) break;
      const line = this.#buffer.slice(0, m.index);
      this.#buffer = this.#buffer.slice(m.index + m[0].length);
      const msg = this.#line(line);
      if (msg) out.push(msg);
    }
    return out;
  }

  #line(line: string): SseMessage | undefined {
    if (line === "") {
      if (this.#data.length === 0) {
        this.#event = undefined;
        return undefined;
      }
      const msg: SseMessage = { data: this.#data.join("\n") };
      if (this.#id !== undefined) msg.id = this.#id;
      if (this.#event !== undefined) msg.event = this.#event;
      this.#data = [];
      this.#event = undefined;
      return msg;
    }
    if (line.startsWith(":")) return undefined;
    const colon = line.indexOf(":");
    const field = colon === -1 ? line : line.slice(0, colon);
    let value = colon === -1 ? "" : line.slice(colon + 1);
    if (value.startsWith(" ")) value = value.slice(1);
    switch (field) {
      case "id":
        if (!value.includes("\0")) this.#id = value;
        break;
      case "event":
        this.#event = value;
        break;
      case "data":
        this.#data.push(value);
        break;
    }
    return undefined;
  }
}
