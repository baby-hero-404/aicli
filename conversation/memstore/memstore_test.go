package memstore

import (
	"testing"

	"github.com/baby-hero-404/aicli/conversation"
	"github.com/baby-hero-404/aicli/conversation/storetest"
)

func TestStore(t *testing.T) {
	storetest.Run(t, func(*testing.T) conversation.Store { return New() })
}
