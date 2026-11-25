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

// Command llmsim is the LLM router simulator: serve, record, proxy-record
// and flavor-check.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"syscall"

	"github.com/saichler/l8opensim/go/llmsim/engine"
	"github.com/saichler/l8opensim/go/llmsim/recorder"
)

const usage = `usage: llmsim <command> [flags]

commands:
  serve          serve the simulated LLM APIs
  record         record the scenario catalog against a real LLM (Halo)
  proxy-record   transparent recording proxy in front of a real LLM
  flavor-check   validate the flavoring of the recorded cassettes

run "llmsim <command> -h" for the flags of a command`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(os.Args[2:])
	case "record":
		err = record(os.Args[2:])
	case "proxy-record":
		err = proxyRecord(os.Args[2:])
	case "flavor-check":
		err = flavorCheck(os.Args[2:])
	case "-h", "--help", "help":
		fmt.Println(usage)
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		log.Fatal(err)
	}
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fs.String("listen", ":9080", "listen address")
	profiles := fs.String("profiles", "profiles", "directory with the profile JSON files")
	profile := fs.String("profile", "default", "profile to start with")
	scenarios := fs.String("scenarios", "scenarios/recorded", "directory with the recorded cassettes")
	auth := fs.String("auth", "on", "API key checks: on or off")
	certFile := fs.String("tls-cert", "", "TLS certificate (PEM); serves HTTPS when set with -tls-key")
	keyFile := fs.String("tls-key", "", "TLS private key (PEM)")
	reusePort := fs.Bool("reuseport", false, "SO_REUSEPORT, to run several llmsim processes on one port")
	minFDs := fs.Uint64("min-fds", 65536, "minimum open-files limit required (0 = no check)")
	traceFile := fs.String("trace-file", "", "sampled JSON-lines trace of matched requests")
	traceSample := fs.Float64("trace-sample", 0.01, "fraction of requests traced")
	_ = fs.Parse(args)
	if *minFDs > 0 {
		if err := engine.CheckFileLimit(*minFDs); err != nil {
			return err
		}
	}
	e, err := engine.New(engine.Options{ProfileDir: *profiles, Profile: *profile, ScenarioDir: *scenarios,
		AuthOff: *auth == "off", TraceFile: *traceFile, TraceSample: *traceSample})
	if err != nil {
		return err
	}
	defer e.Close()
	ln, err := engine.Listen(*listen, *reusePort)
	if err != nil {
		return err
	}
	srv := engine.NewHTTPServer(e.Handler())
	log.Printf("llmsim serving on %s: profile %s, %d cassettes in %d flavors, auth %s",
		*listen, e.ProfileName(), e.Library().Size(), len(e.Library().Flavors()), *auth)
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		_ = srv.Shutdown(context.Background())
	}()
	if *certFile != "" && *keyFile != "" {
		err = srv.ServeTLS(ln, *certFile, *keyFile)
	} else {
		err = srv.Serve(ln)
	}
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func record(args []string) error {
	fs := flag.NewFlagSet("record", flag.ExitOnError)
	cfg := recorder.RunConfig{}
	fs.StringVar(&cfg.CatalogDir, "catalog", "scenarios/catalog", "catalog directory")
	fs.StringVar(&cfg.OutDir, "out", "scenarios/recorded", "output directory for cassettes")
	fs.StringVar(&cfg.TantoFile, "tanto", "scenarios/tanto_tools.json", "Tanto's prompt and tools")
	fs.StringVar(&cfg.FixturesFile, "fixtures", "scenarios/fixtures/models.json", "fixture models for the data tools")
	fs.StringVar(&cfg.ChatURL, "upstream", "https://192.168.1.124:8080/v1", "chat model base URL")
	fs.StringVar(&cfg.MuseURL, "muse-upstream", "https://192.168.1.124:8082/v1", "reasoning model base URL ('' = none)")
	fs.StringVar(&cfg.WebURL, "web-upstream", "https://192.168.1.124:8083", "web tools base URL ('' = synthetic)")
	fs.StringVar(&cfg.ChatModel, "model", "gpt-oss-120b", "chat model name")
	fs.StringVar(&cfg.MuseModel, "muse-model", "muse-glimmer-30b", "reasoning model name")
	fs.IntVar(&cfg.Concurrency, "concurrency", 1, "parallel conversations on the chat model")
	fs.IntVar(&cfg.MaxRounds, "max-rounds", 8, "tool rounds per turn before forcing an answer")
	fs.IntVar(&cfg.MaxTokens, "max-tokens", 8192, "max_tokens per call")
	fs.StringVar(&cfg.Only, "only", "", "record only this flavor")
	fs.IntVar(&cfg.Limit, "limit", 0, "record at most this many conversations (0 = all)")
	ca := fs.String("ca", "../../llm/halo-ca.crt", "CA certificate trusted for the upstream ('' = system CAs)")
	keyEnv := fs.String("key-env", "L8_LLM_KEY", "environment variable holding the upstream API key")
	_ = fs.Parse(args)
	up, err := recorder.NewUpstream(os.Getenv(*keyEnv), *ca)
	if err != nil {
		return err
	}
	cfg.Upstream = up
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	return recorder.Run(ctx, cfg)
}

func proxyRecord(args []string) error {
	fs := flag.NewFlagSet("proxy-record", flag.ExitOnError)
	listen := fs.String("listen", ":9090", "listen address")
	upstream := fs.String("upstream", "https://192.168.1.124:8080/v1", "upstream base URL")
	ca := fs.String("ca", "../../llm/halo-ca.crt", "CA certificate trusted for the upstream ('' = system CAs)")
	out := fs.String("out", "scenarios/recorded", "output directory for cassettes")
	flavor := fs.String("flavor", "proxy", "flavor name given to the recorded conversations")
	keyEnv := fs.String("key-env", "L8_LLM_KEY", "environment variable holding the upstream API key")
	_ = fs.Parse(args)
	up, err := recorder.NewUpstream(os.Getenv(*keyEnv), *ca)
	if err != nil {
		return err
	}
	log.Printf("recording proxy on %s → %s, cassettes in %s/%s", *listen, *upstream, *out, *flavor)
	return http.ListenAndServe(*listen, recorder.NewProxy(*upstream, up, *out, *flavor))
}

func flavorCheck(args []string) error {
	fs := flag.NewFlagSet("flavor-check", flag.ExitOnError)
	scenarios := fs.String("scenarios", "scenarios/recorded", "directory with the recorded cassettes")
	seeds := fs.Int("seeds", 20, "conversations rendered per step")
	verbose := fs.Bool("v", false, "print warnings")
	_ = fs.Parse(args)
	lib, err := engine.LoadLibrary(*scenarios)
	if err != nil {
		return err
	}
	rep := engine.FlavorCheck(lib, *seeds)
	flavors := make([]string, 0, len(rep.ByFlavor))
	for f := range rep.ByFlavor {
		flavors = append(flavors, f)
	}
	sort.Strings(flavors)
	for _, f := range flavors {
		fmt.Printf("  %-16s %d cassettes\n", f, rep.ByFlavor[f])
	}
	fmt.Printf("cassettes %d, steps %d, static steps %d, warnings %d, failures %d\n",
		rep.Cassettes, rep.Steps, rep.Static, len(rep.Warnings), len(rep.Failures))
	if *verbose {
		for _, w := range rep.Warnings {
			fmt.Println("WARN", w)
		}
	}
	for _, f := range rep.Failures {
		fmt.Println("FAIL", f)
	}
	if len(rep.Failures) > 0 {
		return fmt.Errorf("%d flavoring failures", len(rep.Failures))
	}
	return nil
}
