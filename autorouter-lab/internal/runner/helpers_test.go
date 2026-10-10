package runner

import (
	"io"
	"os"
	"strconv"
	"sync"
)

func writeFile(path, body string) error { return os.WriteFile(path, []byte(body), 0o600) }

// formatPerToken renders usdPerMillion as a per-token decimal string.
func formatPerToken(usdPerMillion int) string {
	return strconv.FormatFloat(float64(usdPerMillion)/1e6, 'f', -1, 64)
}

type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}
