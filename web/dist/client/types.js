// src/client/types.ts
/** The server answered with an error. `code` is the stable machine-readable reason. */
var ApiError = class extends Error {
	status;
	code;
	constructor(status, code, message) {
		super(message);
		this.name = "ApiError";
		this.status = status;
		this.code = code;
	}
};
export { ApiError };
