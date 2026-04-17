//go:build unix

package commandtools

import (
	"errors"
	"syscall"
)

func isPTYEOFError(err error) bool {
	return errors.Is(err, syscall.EIO)
}
