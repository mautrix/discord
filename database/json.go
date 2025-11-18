package database

import (
	"go.mau.fi/util/dbutil"
)

// Backported from mautrix/go-util@e5cb5e96d15cb87ffe6e5970c2f90ee47980e715.

// JSONPtr is a convenience function for wrapping a pointer to a value in the JSON utility, but removing typed nils
// (i.e. preventing nils from turning into the string "null" in the database).
func JSONPtr[T any](val *T) dbutil.JSON {
	return dbutil.JSON{Data: UntypedNil(val)}
}

func UntypedNil[T any](val *T) any {
	if val == nil {
		return nil
	}
	return val
}
