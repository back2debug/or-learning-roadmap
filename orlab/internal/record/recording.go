package record

import (
	"bytes"
	"io"
	"net/http"

	"github.com/back2debug/or-learning-roadmap/orlab/internal/redact"
)

// Doer sends one HTTP request.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// RecordingDoer wraps a Doer and writes every completed exchange (any
// method — completions, /models, /generation alike) to the run file as a
// replay cassette. Response bytes stream through untouched; the copy is
// taken as the caller reads.
type RecordingDoer struct {
	Inner Doer
	W     *Writer
}

func (rd *RecordingDoer) Do(req *http.Request) (*http.Response, error) {
	var reqBody []byte
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, err
		}
		reqBody = b
		req.Body = io.NopCloser(bytes.NewReader(b))
	}
	resp, err := rd.Inner.Do(req)
	if err != nil {
		return resp, err
	}
	pathq := PathAndQuery(req)
	status := resp.StatusCode
	headers := redact.Headers(resp.Header)
	resp.Body = CaptureBody(resp.Body, func(raw []byte) {
		// A record refused by fail-closed redaction is counted by the
		// writer and reported at the end of the run.
		_ = rd.W.Write("exchange", Exchange{
			Key:         ExchangeKey(req.Method, pathq, reqBody),
			Method:      req.Method,
			Path:        pathq,
			RequestBody: string(reqBody),
			Status:      status,
			RespHeaders: headers,
			RespBody:    string(raw),
		})
	})
	return resp, nil
}
