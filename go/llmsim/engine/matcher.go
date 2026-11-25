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
	"strconv"
	"strings"
)

// Binding methods, reported in stats and traces.
const (
	BindCompaction = "compaction"
	BindMarker     = "marker"
	BindExact      = "exact"
	BindSession    = "session"
	BindClassifier = "classifier"
	BindMix        = "mix"
	BindNone       = "none"
)

// ConversationSeed is the stateless key of a request: the normalized last
// user message, the number of user messages, the first user message and the
// optional session hint. The same request maps to the same seed on any
// simulator instance.
func ConversationSeed(req *Request) uint64 {
	h := hashString(normalize(req.LastUser))
	h = mix(h, uint64(req.UserCount))
	h = mix(h, hashString(normalize(req.FirstUser)))
	if req.Session != "" {
		h = mix(h, hashString(req.Session))
	}
	return h
}

// conversationKey identifies a conversation (its first user message and the
// optional session hint), independent of how far it has progressed. It keeps
// cassette choice and flavored values stable across the turns of a session.
func conversationKey(req *Request) uint64 {
	h := hashString(normalize(req.FirstUser))
	if req.Session != "" {
		h = mix(h, hashString(req.Session))
	}
	return h
}

type binding struct {
	c      *Cassette
	turn   int
	flavor string
	how    string
}

// bind chooses the cassette and turn that answer the request.
func bind(p *runtimeProfile, lib *Library, req *Request, seed uint64) binding {
	if p.CompactionMarker != "" && strings.Contains(req.System, p.CompactionMarker) {
		return pickInFlavor(p, lib, req, seed, "compaction", BindCompaction)
	}
	if marker := parseMarker(req.LastUser); marker != "" {
		if c := lib.byID[marker]; c != nil {
			return binding{c: c, turn: chooseTurn(c, req.UserCount), flavor: c.Flavor, how: BindMarker}
		}
		if lib.byFlavor[marker] != nil {
			return pickInFlavor(p, lib, req, seed, marker, BindMarker)
		}
	}
	conv := conversationKey(req)
	if refs := lib.byUser[normalize(req.LastUser)]; len(refs) > 0 {
		if same := sameConversation(refs, req); len(same) > 0 {
			ref := same[mix(conv, 5)%uint64(len(same))]
			return binding{c: ref.c, turn: ref.turn, flavor: ref.c.Flavor, how: BindExact}
		}
		ref := refs[mix(seed, 5)%uint64(len(refs))]
		return binding{c: ref.c, turn: ref.turn, flavor: ref.c.Flavor, how: BindExact}
	}
	if req.UserCount > 1 {
		// A follow-up worded differently from the recording: continue the
		// recorded session that the first message started.
		if starts := sessionStarts(lib, req); len(starts) > 0 {
			c := starts[mix(conv, 5)%uint64(len(starts))]
			return binding{c: c, turn: req.UserCount - 1, flavor: c.Flavor, how: BindSession}
		}
	}
	if flavor := classify(p, req); flavor != "" && lib.byFlavor[flavor] != nil {
		return pickInFlavor(p, lib, req, seed, flavor, BindClassifier)
	}
	return pickInFlavor(p, lib, req, seed, mixFlavor(p, lib, seed), BindMix)
}

// sameConversation keeps the refs whose cassette starts with the request's
// first user message at the same position in the conversation.
func sameConversation(refs []cassetteRef, req *Request) []cassetteRef {
	first := normalize(req.FirstUser)
	out := []cassetteRef{}
	for _, r := range refs {
		if r.turn == req.UserCount-1 && len(r.c.turns) > 0 && r.c.turns[0].userNorm == first {
			out = append(out, r)
		}
	}
	return out
}

// sessionStarts returns the multi-turn cassettes that begin with the
// request's first user message and are long enough for its position.
func sessionStarts(lib *Library, req *Request) []*Cassette {
	out := []*Cassette{}
	for _, r := range lib.byUser[normalize(req.FirstUser)] {
		if r.turn == 0 && len(r.c.turns) >= req.UserCount {
			out = append(out, r.c)
		}
	}
	return out
}

// mixFlavor draws a flavor from the profile's weighted mix (only flavors that
// have cassettes), or any flavor when the mix is empty.
func mixFlavor(p *runtimeProfile, lib *Library, seed uint64) string {
	if p.mixTotal > 0 {
		r := unitFloat(mix(seed, 3)) * p.mixTotal
		for _, name := range p.mixNames {
			r -= p.FlavorMix[name]
			if r < 0 && lib.byFlavor[name] != nil {
				return name
			}
		}
		for _, name := range p.mixNames {
			if lib.byFlavor[name] != nil {
				return name
			}
		}
	}
	if len(lib.flavors) == 0 {
		return ""
	}
	return lib.flavors[mix(seed, 4)%uint64(len(lib.flavors))]
}

// pickInFlavor chooses a cassette of a flavor, preferring cassettes recorded
// with the requested model and honoring the profile's flawed fraction.
func pickInFlavor(p *runtimeProfile, lib *Library, req *Request, seed uint64, flavor, how string) binding {
	set := lib.byFlavor[flavor]
	if set == nil {
		if flavor = mixFlavor(p, lib, seed); flavor == "" {
			return binding{how: BindNone}
		}
		set = lib.byFlavor[flavor]
	}
	list := set.ok
	if len(set.flawed) > 0 && (len(list) == 0 || unitFloat(mix(seed, 11)) < p.FlawedFraction) {
		list = set.flawed
	}
	n := 0
	for _, c := range list {
		if c.Model == req.Model {
			n++
		}
	}
	idx := mix(seed, 6)
	var c *Cassette
	if n > 0 {
		k := int(idx % uint64(n))
		for _, cand := range list {
			if cand.Model == req.Model {
				if k == 0 {
					c = cand
					break
				}
				k--
			}
		}
	} else {
		c = list[idx%uint64(len(list))]
	}
	return binding{c: c, turn: chooseTurn(c, req.UserCount), flavor: flavor, how: how}
}

// chooseTurn maps the n-th user message of a conversation to a cassette turn.
func chooseTurn(c *Cassette, userCount int) int {
	t := userCount - 1
	if t < 0 {
		t = 0
	}
	if t >= len(c.turns) {
		t = len(c.turns) - 1
	}
	return t
}

// answer builds the flavored reply for a request.
func answer(p *runtimeProfile, lib *Library, req *Request, seed uint64) (*Reply, string, int) {
	b := bind(p, lib, req, seed)
	if b.c == nil {
		return genericReply(p, req, b.flavor), b.how, 0
	}
	// Flavored values come from the conversation key so names, numbers and
	// rows stay the same across the turns of one session.
	rc := newRenderCtx(mix(conversationKey(req), hashString(b.c.ID)), b.c, req.LastUser)
	t := b.c.turns[b.turn]
	s := req.ToolStep
	if s >= len(t.steps) {
		s = len(t.steps) - 1
	}
	st := t.steps[s]
	rep := &Reply{Flavor: b.flavor, CassetteID: b.c.ID, Turn: b.turn, Step: s}
	subs := 0
	if len(st.calls) > 0 && !req.NoTools {
		calls := make([]OutCall, 0, len(st.calls))
		for i, call := range st.calls {
			args, _ := call.args.render(rc).(map[string]interface{})
			calls = append(calls, OutCall{ID: callID(req.API, seed, s, i), Name: call.name, Args: args})
		}
		calls, subs = fitCalls(calls, req, seed)
		if len(calls) > 0 {
			rep.Calls = calls
			rep.Text = st.text.RenderString(rc)
			rep.Reasoning = st.reasoning.RenderString(rc)
			rep.Finish = FinishToolCalls
			finishTokens(p, rep, st, req)
			return rep, b.how, subs
		}
	}
	if len(st.calls) > 0 || st.src.Text == "" {
		if ft := t.finalTextStep(); ft != nil {
			st = ft
		} else {
			g := genericReply(p, req, b.flavor)
			g.Flavor, g.CassetteID, g.Turn, g.Step = b.flavor, b.c.ID, b.turn, s
			return g, b.how, subs
		}
	}
	rep.Text = st.text.RenderString(rc)
	rep.Reasoning = st.reasoning.RenderString(rc)
	rep.Finish = st.src.FinishReason
	if rep.Finish == "" || rep.Finish == FinishToolCalls {
		rep.Finish = FinishStop
	}
	if strings.Contains(rep.Text, "#sim:") {
		rep.Text = stripMarker(rep.Text)
	}
	finishTokens(p, rep, st, req)
	return rep, b.how, subs
}

// finishTokens fills usage: prompt tokens are estimated from the request
// size; completion tokens are the recorded count rescaled to the rendered
// length.
func finishTokens(p *runtimeProfile, rep *Reply, st *compiledStep, req *Request) {
	rep.PromptTokens = estimateTokens(p, req.BodyLen)
	out := len(rep.Text) + len(rep.Reasoning)
	for _, c := range rep.Calls {
		out += len(c.Name) + len(callArgs(&c))
	}
	rec := st.src.Usage.CompletionTokens
	if rec > 0 && st.origChars > 0 {
		rep.CompletionTokens = int(float64(rec) * float64(out) / float64(st.origChars))
	} else {
		rep.CompletionTokens = int(float64(out) / p.CharsPerToken)
	}
	if rep.CompletionTokens < 1 {
		rep.CompletionTokens = 1
	}
}

func estimateTokens(p *runtimeProfile, chars int) int {
	n := int(float64(chars) / p.CharsPerToken)
	if n < 1 {
		n = 1
	}
	return n
}

func callID(api string, seed uint64, step, i int) string {
	prefix := "call_"
	if api == APIAnthropic {
		prefix = "toolu_"
	}
	return prefix + strconv.FormatUint(mix(seed, uint64(step*64+i+1)), 36)
}
