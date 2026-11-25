//go:build linux

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

import "syscall"

// soReusePort is SO_REUSEPORT on Linux (not exported by package syscall).
const soReusePort = 0xf

func setReusePort(fd uintptr) error {
	return syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, soReusePort, 1)
}

func fileLimit() (uint64, uint64, error) {
	var r syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &r); err != nil {
		return 0, 0, err
	}
	return r.Cur, r.Max, nil
}

func raiseFileLimit(to uint64) error {
	var r syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &r); err != nil {
		return err
	}
	r.Cur = to
	return syscall.Setrlimit(syscall.RLIMIT_NOFILE, &r)
}
