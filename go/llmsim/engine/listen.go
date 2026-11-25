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
	"context"
	"fmt"
	"net"
	"net/http"
	"syscall"
	"time"
)

// Listen opens the server socket. With reusePort, several llmsim processes
// can listen on the same port and the kernel spreads connections across them.
func Listen(addr string, reusePort bool) (net.Listener, error) {
	lc := net.ListenConfig{}
	if reusePort {
		lc.Control = func(network, address string, c syscall.RawConn) error {
			var serr error
			err := c.Control(func(fd uintptr) {
				serr = setReusePort(fd)
			})
			if err != nil {
				return err
			}
			return serr
		}
	}
	return lc.Listen(context.Background(), "tcp", addr)
}

// NewHTTPServer returns a server tuned for many keep-alive clients.
func NewHTTPServer(h http.Handler) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       5 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}
}

// CheckFileLimit raises the open-files soft limit to the hard limit and
// fails when it is still below min.
func CheckFileLimit(min uint64) error {
	cur, max, err := fileLimit()
	if err != nil {
		return nil // not supported on this platform
	}
	if cur < max {
		if raiseFileLimit(max) == nil {
			cur = max
		}
	}
	if cur < min {
		return fmt.Errorf("open files limit is %d, need at least %d for this many sessions: raise it with "+
			"'ulimit -n %d' or increase_file_limits.sh", cur, min, min)
	}
	return nil
}
