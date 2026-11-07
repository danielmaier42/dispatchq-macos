//go:build unix

package proctree

import "errors"

var errMalformed = errors.New("proctree: unreadable process entry")
