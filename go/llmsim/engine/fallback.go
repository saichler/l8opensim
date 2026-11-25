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

// wrapUps are the generic final answers used when a cassette has no text step
// to give (for example tool_choice "none" in the middle of a tool loop) or
// when the library has no cassette at all.
var wrapUps = map[string]string{
	"data_query":     "Here is what I found in the data. Some details may be incomplete; ask me to narrow the query if you need more.",
	"data_aggregate": "Here are the totals I could compute from the data. Let me know if you want them grouped differently.",
	"csv_export":     "I prepared the export you asked for. Let me know if you need other columns or a different filter.",
	"report_file":    "I prepared the report from the data I could read. Tell me if you want more detail in any section.",
	"record_crud":    "I could not finish the change. Please confirm the record and the fields you want to change.",
	"explore_models": "These are the kinds of data I can work with. Ask me about any of them.",
	"web_research":   "Based on the sources I read, here is a summary. Some details may have changed since they were published.",
	"compaction":     "The user and the assistant discussed the data and the user's requests; no open questions remain.",
}

const defaultWrapUp = "I have answered with the information I have. Let me know if you need anything else."

// genericReply is the answer when no cassette applies.
func genericReply(p *runtimeProfile, req *Request, flavor string) *Reply {
	text, ok := wrapUps[flavor]
	if !ok {
		text = defaultWrapUp
	}
	rep := &Reply{Text: text, Finish: FinishStop, Flavor: flavor}
	rep.PromptTokens = estimateTokens(p, req.BodyLen)
	rep.CompletionTokens = estimateTokens(p, len(text))
	return rep
}
