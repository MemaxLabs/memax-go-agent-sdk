//go:build !unix

package commandtools

func isPTYEOFError(error) bool {
	return false
}
