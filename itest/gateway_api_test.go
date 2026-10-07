package itest

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/testrig"
)

// api sends the Instance token, with the origin headers, to the recorded URL and
// reports the answer parsed; the token never goes anywhere else.
func TestGatewayAPICallsTheInstanceWithItsToken(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir, stamp := gatewayFixture(t, env, testrig.MinimalContract)
	stub := testrig.ServeGateway(t, stamp.Ports.HTTP, map[string]int{"/data/api/v1/gateway-info": 200, "/data/api/v1/x": 404})
	stub.SetBody("/data/api/v1/gateway-info", `{"name":"igdev-gw","version":"8.3.8"}`)
	token := gatewayToken(t, env, dir)

	res := env.RunIn(dir, "gateway", "api", "get", "/data/api/v1/gateway-info", "--header", "X-Trace: 7", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "gateway_api.json", res.Stdout)
	verb, headers, _ := stub.LastRequest()
	for name, want := range map[string]string{
		"X-Ignition-Api-Token": token, "Origin": "http://127.0.0.1:" + strconv.Itoa(stamp.Ports.HTTP),
		"Accept": "application/json", "X-Trace": "7",
	} {
		if got := headers.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if verb != http.MethodGet {
		t.Errorf("method = %s", verb)
	}
	if strings.Contains(res.Stdout+res.Stderr, token) {
		t.Error("the token is in igdev's output")
	}

	human := env.Run(testrig.Run{Dir: dir, Args: []string{"gateway", "api", "POST", "/data/api/v1/gateway-info", "--data", "-"}, Stdin: `{"a":1}`})
	testrig.WantExit(t, human, contract.ExitOK)
	if !strings.Contains(human.Stdout, `"igdev-gw"`) {
		t.Errorf("human mode did not print the body: %q", human.Stdout)
	}
	if _, h, body := stub.LastRequest(); body != `{"a":1}` || h.Get("Content-Type") != "application/json" {
		t.Errorf("the body or its type did not arrive: %q %q", body, h.Get("Content-Type"))
	}

	failed := env.RunIn(dir, "gateway", "api", "GET", "/data/api/v1/x", "--json")
	testrig.WantExit(t, failed, contract.ExitFailure)
	testrig.WantCode(t, testrig.Envelope(t, failed.Stdout), contract.CodeGatewayAPI)
}

// A full URL, another host, or a header that would replace the token is a usage
// error, and nothing is sent.
func TestGatewayAPIRefusesToSendTheTokenElsewhere(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir, stamp := gatewayFixture(t, env, testrig.MinimalContract)
	stub := testrig.ServeGateway(t, stamp.Ports.HTTP, nil)
	for _, args := range [][]string{
		{"GET", "http://evil.example/data"},
		{"GET", "//evil.example/data"},
		{"GET", "/x", "--header", "X-Ignition-API-Token: other"},
		{"GET", "/x", "--header", "Host: evil.example"},
		{"FETCH", "/x"},
	} {
		res := env.RunIn(dir, append([]string{"gateway", "api"}, append(args, "--json")...)...)
		testrig.WantExit(t, res, contract.ExitUsage)
	}
	if stub.Hits() != 0 {
		t.Errorf("a refused call still reached the Gateway %d times", stub.Hits())
	}
}

// An answer over the cap is reported with its full size, not cut silently.
func TestGatewayAPIReportsATruncatedAnswer(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir, stamp := gatewayFixture(t, env, testrig.MinimalContract)
	stub := testrig.ServeGateway(t, stamp.Ports.HTTP, map[string]int{"/big": 200})
	stub.SetBody("/big", strings.Repeat("x", 5<<20))
	res := env.RunIn(dir, "gateway", "api", "GET", "/big", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	var data struct {
		BodyBytes int64 `json:"body_bytes"`
		Truncated bool  `json:"truncated"`
	}
	testrig.DataOf(t, res.Stdout, &data)
	if !data.Truncated || data.BodyBytes != 5<<20 {
		t.Errorf("truncated = %v, body_bytes = %d, want true and %d", data.Truncated, data.BodyBytes, 5<<20)
	}
}

// --output writes the whole answer to a file, past the 4 MiB print cap, and the
// report carries where and how much instead of the body.
func TestGatewayAPIOutputWritesTheWholeAnswer(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir, stamp := gatewayFixture(t, env, testrig.MinimalContract)
	stub := testrig.ServeGateway(t, stamp.Ports.HTTP, map[string]int{"/big": 200, "/missing": 404})
	body := `{"x":"` + strings.Repeat("y", 5<<20) + `"}`
	stub.SetBody("/big", body)
	out := filepath.Join(dir, "big.json")
	res := env.RunIn(dir, "gateway", "api", "GET", "/big", "--output", "big.json", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	var data struct {
		Output    string `json:"output"`
		BodyBytes int64  `json:"body_bytes"`
		Body      any    `json:"body"`
	}
	testrig.DataOf(t, res.Stdout, &data)
	written, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != body || data.BodyBytes != int64(len(body)) || data.Output != out || data.Body != nil {
		t.Errorf("output = %s (%d bytes reported, %d written), body = %v", data.Output, data.BodyBytes, len(written), data.Body)
	}
	if strings.Contains(res.Stdout, "yyyy") {
		t.Error("the body was printed as well as written")
	}

	// A refused call writes no file.
	failed := env.RunIn(dir, "gateway", "api", "GET", "/missing", "--output", "missing.json", "--json")
	testrig.WantExit(t, failed, contract.ExitFailure)
	if _, err := os.Stat(filepath.Join(dir, "missing.json")); !os.IsNotExist(err) {
		t.Errorf("a 404 left a file behind: %v", err)
	}
}
