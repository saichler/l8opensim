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
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// SearchResult is one web search hit.
type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// FetchResult is a fetched page (or one page of it).
type FetchResult struct {
	Title      string `json:"title"`
	Text       string `json:"text"`
	TotalChars int    `json:"total_chars"`
	Truncated  bool   `json:"truncated"`
	NextStart  *int   `json:"next_start"`
}

func (e *Engine) serveSearch(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	e.stats.total.Add(1)
	e.stats.byAPI.inc("web_search")
	p := e.prof.Load()
	if _, status, msg := e.authorize(p, r, APIWebTools); status != 0 {
		e.fail(w, APIWebTools, start, status, msg, 0)
		return
	}
	in := struct {
		Query      string `json:"query"`
		MaxResults int    `json:"max_results"`
	}{}
	if !e.decodeTool(w, r, start, &in) {
		return
	}
	if strings.TrimSpace(in.Query) == "" {
		e.fail(w, APIWebTools, start, http.StatusBadRequest, "query is required", 0)
		return
	}
	if in.MaxResults <= 0 {
		in.MaxResults = 8
	}
	if in.MaxResults > 20 {
		in.MaxResults = 20
	}
	results := e.recordedSearch(in.Query)
	if results == nil {
		results = SyntheticSearch(in.Query, in.MaxResults)
	}
	if len(results) > in.MaxResults {
		results = results[:in.MaxResults]
	}
	e.writeTool(w, start, map[string]interface{}{"results": results})
}

func (e *Engine) recordedSearch(query string) []SearchResult {
	raw, ok := e.lib.search[normalize(query)]
	if !ok {
		return nil
	}
	out := struct {
		Results []SearchResult `json:"results"`
	}{}
	if json.Unmarshal([]byte(raw), &out) != nil || len(out.Results) == 0 {
		return nil
	}
	return out.Results
}

func (e *Engine) serveFetch(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	e.stats.total.Add(1)
	e.stats.byAPI.inc("fetch_url")
	p := e.prof.Load()
	if _, status, msg := e.authorize(p, r, APIWebTools); status != 0 {
		e.fail(w, APIWebTools, start, status, msg, 0)
		return
	}
	in := struct {
		URL      string `json:"url"`
		MaxChars int    `json:"max_chars"`
		Start    int    `json:"start"`
	}{}
	if !e.decodeTool(w, r, start, &in) {
		return
	}
	if reason := blockedURL(in.URL); reason != "" {
		e.fail(w, APIWebTools, start, http.StatusBadRequest, reason, 0)
		return
	}
	if in.MaxChars <= 0 {
		in.MaxChars = 20000
	}
	in.MaxChars = clampInt(in.MaxChars, 500, 100000)
	title, text := e.recordedFetch(in.URL)
	if text == "" {
		title, text = SyntheticPage(in.URL)
	}
	e.writeTool(w, start, PageSlice(title, text, in.Start, in.MaxChars))
}

func (e *Engine) recordedFetch(u string) (string, string) {
	raw, ok := e.lib.fetch[u]
	if !ok {
		return "", ""
	}
	out := FetchResult{}
	if json.Unmarshal([]byte(raw), &out) != nil {
		return "", ""
	}
	return out.Title, out.Text
}

func (e *Engine) decodeTool(w http.ResponseWriter, r *http.Request, start time.Time, v interface{}) bool {
	body, err := readBody(r)
	if err != nil {
		e.fail(w, APIWebTools, start, http.StatusBadRequest, err.Error(), 0)
		return false
	}
	err = json.Unmarshal(body.Bytes(), v)
	putBuffer(body)
	if err != nil {
		e.fail(w, APIWebTools, start, http.StatusBadRequest, "invalid JSON body: "+err.Error(), 0)
		return false
	}
	return true
}

func (e *Engine) writeTool(w http.ResponseWriter, start time.Time, v interface{}) {
	buf := getBuffer()
	_ = json.NewEncoder(buf).Encode(v)
	n := buf.Len()
	writeRaw(w, http.StatusOK, buf.Bytes())
	putBuffer(buf)
	e.stats.observe(time.Since(start), http.StatusOK, n)
}

// PageSlice returns one page of a text, as the fetch tool does.
func PageSlice(title, text string, start, maxChars int) FetchResult {
	if start < 0 || start > len(text) {
		start = 0
	}
	end := start + maxChars
	res := FetchResult{Title: title, TotalChars: len(text)}
	if end < len(text) {
		res.Truncated = true
		next := end
		res.NextStart = &next
	} else {
		end = len(text)
	}
	res.Text = text[start:end]
	return res
}

// SyntheticSearch builds well-formed results for a query that was never
// recorded.
func SyntheticSearch(query string, n int) []SearchResult {
	h := hashString(normalize(query))
	slug := strings.ToLower(strings.Join(strings.Fields(query), "-"))
	if len(slug) > 60 {
		slug = slug[:60]
	}
	sites := []string{"docs.example.com", "blog.example.org", "wiki.example.net", "news.example.com",
		"dev.example.io", "forum.example.org", "guide.example.com", "reference.example.dev"}
	out := make([]SearchResult, 0, n)
	for i := 0; i < n; i++ {
		site := sites[(h+uint64(i))%uint64(len(sites))]
		out = append(out, SearchResult{
			Title:   strings.TrimSpace(query) + " — " + strings.Title(pick(loremWords, mix(h, uint64(i)))),
			URL:     "https://" + site + "/" + slug + "-" + itoa(i+1),
			Snippet: SyntheticText(mix(h, uint64(100+i)), 160),
		})
	}
	return out
}

// SyntheticPage builds a deterministic page for a URL that was never recorded.
func SyntheticPage(u string) (string, string) {
	h := hashString(u)
	title := "Page " + strings.Title(pick(loremWords, h)) + " " + strings.Title(pick(loremWords, mix(h, 1)))
	return title, SyntheticText(h, 6000+int(h%6000))
}

// blockedURL mirrors the real fetch tool: only public http(s) hosts.
func blockedURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "invalid URL: only http and https URLs are allowed"
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return "blocked: target address is not a public internet address"
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
			ip.IsUnspecified() || ip.IsMulticast() || host == "169.254.169.254" {
			return "blocked: target address is not a public internet address"
		}
	}
	return ""
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
