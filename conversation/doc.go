// Package conversation turns headless CLI runs into resumable, streamable conversations. A
// Service creates conversations, runs one CLI turn per message (resuming the CLI's own
// session), records every event so history can be replayed and exported, and lets many viewers
// follow a turn live.
//
// The host application owns identity, authorization and storage by implementing three small
// interfaces: Authorizer (who may do what), Resolver (how a turn is executed: workspace, image,
// credentials) and Store (durable state, with an atomic turn claim). The package knows nothing
// about the host's users or database. Nothing a client sends can influence how the CLI is
// executed: the only inputs are an allowlisted profile name and the message text.
package conversation
