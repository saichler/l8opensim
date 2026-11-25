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
	"bufio"
	"encoding/json"
	"math/rand/v2"
	"os"
	"strconv"
	"sync"
	"time"
)

// tracer writes a sampled JSON-lines log of matched requests, so a client
// failure can be reproduced by resending the same request.
type tracer struct {
	mtx    sync.Mutex
	f      *os.File
	w      *bufio.Writer
	sample float64
}

type traceLine struct {
	Time     string `json:"time"`
	API      string `json:"api"`
	Model    string `json:"model"`
	Key      string `json:"conversation_key"`
	Flavor   string `json:"flavor"`
	Binding  string `json:"binding"`
	Cassette string `json:"cassette"`
	Turn     int    `json:"turn"`
	Step     int    `json:"step"`
	Status   int    `json:"status"`
	Fault    string `json:"fault,omitempty"`
}

func newTracer(path string, sample float64) (*tracer, error) {
	if path == "" || sample <= 0 {
		return nil, nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	t := &tracer{f: f, w: bufio.NewWriterSize(f, 64*1024), sample: sample}
	go t.flushLoop()
	return t, nil
}

func (t *tracer) flushLoop() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for range tick.C {
		t.mtx.Lock()
		_ = t.w.Flush()
		t.mtx.Unlock()
	}
}

func (t *tracer) record(req *Request, seed uint64, rep *Reply, how string, status int) {
	if t == nil || rand.Float64() >= t.sample {
		return
	}
	line := traceLine{Time: time.Now().UTC().Format(time.RFC3339Nano), API: req.API, Model: req.Model,
		Key: strconv.FormatUint(seed, 36), Binding: how, Status: status}
	if rep != nil {
		line.Flavor, line.Cassette, line.Turn, line.Step, line.Fault =
			rep.Flavor, rep.CassetteID, rep.Turn, rep.Step, rep.Fault
	}
	data, _ := json.Marshal(&line)
	t.mtx.Lock()
	_, _ = t.w.Write(data)
	_ = t.w.WriteByte('\n')
	t.mtx.Unlock()
}

func (t *tracer) close() {
	if t == nil {
		return
	}
	t.mtx.Lock()
	_ = t.w.Flush()
	_ = t.f.Close()
	t.mtx.Unlock()
}
