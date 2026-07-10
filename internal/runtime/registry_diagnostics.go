package runtime

import (
	"fmt"
	"os"
	"sync/atomic"

	"github.com/foobarto/stado/internal/textutil"
)

var registryDiagnosticsSuppressed atomic.Int32

func withRegistryDiagnosticsSuppressed(fn func()) {
	registryDiagnosticsSuppressed.Add(1)
	defer registryDiagnosticsSuppressed.Add(-1)
	fn()
}

func emitRegistryDiagnostic(format string, args ...any) {
	if registryDiagnosticsSuppressed.Load() > 0 {
		return
	}
	message := textutil.SanitizeForTerminal(fmt.Sprintf(format, args...))
	_, _ = fmt.Fprint(os.Stderr, message)
}
