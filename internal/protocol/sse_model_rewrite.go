package protocol

import (
	"bufio"
	"io"
	"strings"
)

// NewSSEModelRewriteReader rewrites the upstream model id to the public
// alias inside an OpenAI-style SSE stream ("data: {...}" lines), so clients
// always see the alias they requested — matching the non-streaming
// response, which rewriteResponseModel already normalizes.
//
// Line-buffered: JSON split across network reads is never touched mid-line,
// and every other byte (event boundaries, comments, [DONE]) passes through
// verbatim.
func NewSSEModelRewriteReader(src io.ReadCloser, upstreamModel, alias string) io.ReadCloser {
	if upstreamModel == "" || alias == "" || upstreamModel == alias {
		return src
	}
	pr, pw := io.Pipe()
	go func() {
		defer src.Close()
		defer pw.Close()

		scanner := bufio.NewScanner(src)
		scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
		fromQuoted := `"model":"` + upstreamModel + `"`
		toQuoted := `"model":"` + alias + `"`
		fromSpaced := `"model": "` + upstreamModel + `"`
		toSpaced := `"model": "` + alias + `"`
		for scanner.Scan() {
			line := scanner.Text()
			if strings.Contains(line, fromQuoted) {
				line = strings.ReplaceAll(line, fromQuoted, toQuoted)
			} else if strings.Contains(line, fromSpaced) {
				line = strings.ReplaceAll(line, fromSpaced, toSpaced)
			}
			if _, err := pw.Write([]byte(line + "\n")); err != nil {
				return
			}
		}
	}()
	return pr
}
