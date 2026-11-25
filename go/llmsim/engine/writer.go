/*
 * © 2025 Sharon Aicler (saichler@gmail.com)
 *
 * Layer 8 Ecosystem is licensed under the Apache License, Version 2.0.
 * You may obtain a copy of the License at:
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync"
)

// maxBody is the largest request body accepted (Tanto's requests carry the
// system prompt, the catalog and the tool definitions).
const maxBody = 32 << 20

var bufferPool = sync.Pool{New: func() interface{} { return bytes.NewBuffer(make([]byte, 0, 64*1024)) }}

func getBuffer() *bytes.Buffer {
	b := bufferPool.Get().(*bytes.Buffer)
	b.Reset()
	return b
}

func putBuffer(b *bytes.Buffer) {
	if b.Cap() > 4<<20 {
		return // do not keep very large buffers alive
	}
	bufferPool.Put(b)
}

// readBody reads the request body into a pooled buffer.
func readBody(r *http.Request) (*bytes.Buffer, error) {
	b := getBuffer()
	if r.ContentLength > 0 && r.ContentLength < maxBody {
		b.Grow(int(r.ContentLength))
	}
	_, err := b.ReadFrom(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		putBuffer(b)
		return nil, errors.New("cannot read the request body: " + err.Error())
	}
	if b.Len() > maxBody {
		putBuffer(b)
		return nil, errors.New("request body too large")
	}
	return b, nil
}

// writeRaw writes a JSON body with its length, so keep-alive connections
// stay cheap.
func writeRaw(w http.ResponseWriter, status int, body []byte) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	buf := getBuffer()
	enc := json.NewEncoder(buf)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
	writeRaw(w, status, buf.Bytes())
	putBuffer(buf)
}

func writeSimpleError(buf *bytes.Buffer, msg string) {
	_ = json.NewEncoder(buf).Encode(map[string]string{"error": msg})
}

// dropConnection closes the client connection without a response.
func dropConnection(w http.ResponseWriter) {
	if hj, ok := w.(http.Hijacker); ok {
		if conn, _, err := hj.Hijack(); err == nil {
			_ = conn.Close()
			return
		}
	}
	panic(http.ErrAbortHandler)
}

func itoa(n int) string { return strconv.Itoa(n) }
