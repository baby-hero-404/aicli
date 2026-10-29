package conversation

import (
	"context"

	"github.com/baby-hero-404/aicli/run"
)

// Resolver supplies everything about how a turn is executed. The host implements it, from
// server-side state only: nothing in it may come from a client request.
type Resolver interface {
	// Resolve returns the base run request for one turn: workspace, image, network mode,
	// timeouts, credentials, session directory, environment. The Service overwrites Profile,
	// Command, Args, ExtraArgs, Prompt, ResumeSessionID and OnEvent, so the profile, argument
	// list and prompt cannot be influenced here by mistake. SessionDir must be stable per
	// conversation: it is where the CLI keeps the session that later turns resume.
	Resolve(ctx context.Context, p Principal, c Conversation) (run.Request, error)

	// SaveCredentials persists credential files the CLI refreshed during a turn
	// (run.Result.UpdatedCredentialFiles, keyed as run reports them). It is called only when
	// there is something to save, before the turn is finished, so the next Resolve returns the
	// fresh token. The files are secrets: do not log them.
	SaveCredentials(ctx context.Context, p Principal, c Conversation, files map[string]string) error
}
