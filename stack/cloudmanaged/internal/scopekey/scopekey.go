package scopekey

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/MemaxLabs/memax-go-agent-sdk/tenant"
)

// Digest returns a stable hash for one tenant scope.
func Digest(scope tenant.Scope) string {
	scope = scope.Clone()
	var builder strings.Builder
	builder.WriteString(scope.ID)
	builder.WriteByte('\n')
	builder.WriteString(scope.SubjectID)
	if len(scope.Attributes) > 0 {
		keys := make([]string, 0, len(scope.Attributes))
		for key := range scope.Attributes {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			builder.WriteByte('\n')
			builder.WriteString(key)
			builder.WriteByte('=')
			builder.WriteString(scope.Attributes[key])
		}
	}
	sum := sha256.Sum256([]byte(builder.String()))
	return hex.EncodeToString(sum[:])
}
