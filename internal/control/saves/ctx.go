package saves

import "context"

// ctxBG returns a background context for storage authorization calls.
// Presigning is local cryptography (no I/O); callers with deadlines can
// extend the manager to thread ctx through later.
func ctxBG() context.Context { return context.Background() }
