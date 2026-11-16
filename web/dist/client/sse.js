// src/client/sse.ts
/**
* Incremental parser for text/event-stream. Feed it text as it arrives, in chunks of any size;
* it returns the messages completed by that chunk. Comment lines and unknown fields are
* ignored; a message without data is not dispatched, as the spec requires.
*/
var SseParser = class {
	#buffer = "";
	#id;
	#event;
	#data = [];
	#sawCR = false;
	feed(chunk) {
		const out = [];
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
	#line(line) {
		if (line === "") {
			if (this.#data.length === 0) {
				this.#event = void 0;
				return;
			}
			const msg = { data: this.#data.join("\n") };
			if (this.#id !== void 0) msg.id = this.#id;
			if (this.#event !== void 0) msg.event = this.#event;
			this.#data = [];
			this.#event = void 0;
			return msg;
		}
		if (line.startsWith(":")) return void 0;
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
			case "data": this.#data.push(value);
		}
	}
};
export { SseParser };
