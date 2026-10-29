package conversation

import "context"

// Authorizer decides whether a principal may perform an action. The host implements it.
//
// For ActionCreate and ActionList, c is nil. For every other action c is the conversation
// being acted on. Return nil to allow; any error denies. The Service turns a denial on an
// existing conversation into ErrNotFound so that existence is not revealed.
type Authorizer interface {
	Authorize(ctx context.Context, p Principal, a Action, c *Conversation) error
}

// DenyAll is the Authorizer used when none is configured: everything is denied.
type DenyAll struct{}

// Authorize implements Authorizer.
func (DenyAll) Authorize(context.Context, Principal, Action, *Conversation) error {
	return ErrForbidden
}

// OwnerOnly allows a principal to create and list, and to act on conversations it owns. It is
// a reasonable default for hosts where each conversation belongs to one user.
type OwnerOnly struct{}

// Authorize implements Authorizer.
func (OwnerOnly) Authorize(_ context.Context, p Principal, a Action, c *Conversation) error {
	if p == "" {
		return ErrForbidden
	}
	if c == nil {
		if a == ActionCreate || a == ActionList {
			return nil
		}
		return ErrForbidden
	}
	if c.Owner != p {
		return ErrForbidden
	}
	return nil
}
