// Package httpapi exposes a conversation.Service as a REST + Server-Sent Events API that a host
// mounts into its own router:
//
//	h, err := httpapi.NewHandler(httpapi.Config{Service: svc, Authenticate: authFromRequest})
//	mux.Handle("/api/aicli/", http.StripPrefix("/api/aicli", h))
//
// The host supplies authentication (Config.Authenticate returns an opaque principal) and,
// through the Service, authorization and storage. The handler never touches the host's
// database and never accepts anything from a client that could change how a CLI is executed:
// request bodies are decoded strictly and carry only a profile name, a title, a message and an
// idempotency key.
package httpapi
