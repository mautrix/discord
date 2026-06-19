package connector

import "errors"

// errNotImplemented is returned by Group 1 stub methods that have not been
// filled in yet. Later groups replace these with real implementations.
var errNotImplemented = errors.New("not implemented")
