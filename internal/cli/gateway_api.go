package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sheon-sek/igdev/internal/atomicfile"
	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/gatewayapi"
)

// The bounds of one `gateway api` call: a request body larger than the first is
// refused, and a --json response larger than the second is reported as
// truncated, never cut silently. Human mode and --output stream the whole body.
const (
	apiRequestLimit  = 1 << 30
	apiResponseLimit = 4 << 20
	apiTimeout       = 60 * time.Second
)

// apiResponseHeaders are the response headers `gateway api --json` reports.
var apiResponseHeaders = []string{"Content-Type", "Content-Length", "Location", "Etag"}

// gatewayAPIData is what `gateway api` reports.
type gatewayAPIData struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	URL    string `json:"url"`
	Status int    `json:"status"`
	// Headers are the selected response headers that were present.
	Headers map[string]string `json:"headers"`
	// Body is the response parsed as JSON when it is JSON, otherwise its text,
	// or null when it was empty.
	Body any `json:"body"`
	// BodyBytes is the full response size, read to the end even past the cap.
	BodyBytes int64 `json:"body_bytes"`
	// Truncated is true when the body was larger than the cap: Body then holds
	// the first bytes as text, and BodyBytes the full size.
	Truncated bool `json:"truncated"`
}

// gatewayAPIFileData is what `gateway api --output` reports: the body went to a
// file, whole, so the report carries where and how much instead of the body.
type gatewayAPIFileData struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	URL     string            `json:"url"`
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	// Output is the absolute path of the file the body was written to.
	Output    string `json:"output"`
	BodyBytes int64  `json:"body_bytes"`
}

func (a *App) newGatewayAPICmd() *cobra.Command {
	var (
		data    string
		headers []string
		output  string
	)
	cmd := &cobra.Command{
		Use:   "api <METHOD> <path>",
		Short: "Call this Instance's Gateway REST API with its own token",
		Long: `api sends one request to this Instance's Gateway, at the recorded loopback URL, with
the Instance API token (ADR 0007) and the Origin, Referer and Accept headers the
Gateway's own web UI sends. The path must start with /: a full URL or another host is
refused, so the token never leaves this Instance. --header adds a header; it cannot
replace X-Ignition-API-Token, Origin, Referer or Host.

--data sends a body: @file reads a file, - reads stdin, anything else is sent as
given, as application/json unless a --header names another Content-Type. A body over
1 GiB is refused.

Human mode prints the whole response body and fails on a 4xx or 5xx answer. --json
reports {method, path, url, status, headers, body, body_bytes, truncated}: body is
parsed when it is JSON, and a response over 4 MiB is reported as truncated with its
full size rather than cut silently.

--output <file> streams the whole body into the file instead, with no size cap,
and replaces the file in one rename once the body has arrived, so a failed or
refused call leaves no partial file. --json then reports {method, path, url,
status, headers, output, body_bytes} without the body. Use it for anything larger
than 4 MiB, such as the Gateway's own OpenAPI document. --output alone names the
file after the path's last segment: GET /openapi.json --output writes openapi.json.`,
		Example: `  igdev gateway api GET /data/api/v1/gateway-info
  igdev gateway api GET /data/api/v1/resources/names/ignition/tag-provider --json
  igdev gateway api PUT /data/api/v1/resources/ignition/tag-provider --data @provider.json
  igdev gateway api GET /openapi.json --output openapi.json
  igdev gateway api GET /openapi.json --output`,
		RunE: func(_ *cobra.Command, args []string) error {
			if output == outputFromPath {
				output, args = apiOutputFile(args)
			}
			if len(args) < 2 {
				return missingArgument("gateway api", "path",
					"igdev gateway api GET /data/api/v1/gateway-info", "name the method and the Gateway path")
			}
			if len(args) > 2 {
				return extraArguments("gateway api", 2, args)
			}
			method, path := strings.ToUpper(args[0]), args[1]
			if output == outputFromPath {
				output = apiDefaultOutput(path)
			}
			if !validMethod(method) {
				return contract.UsageFault(fmt.Sprintf("%q is not an HTTP method", args[0]),
					contract.Remediation{Command: "igdev help gateway api", Why: "use GET, POST, PUT, PATCH, DELETE, HEAD or OPTIONS"})
			}
			if reason := gatewayapi.PathProblem(path); reason != "" {
				return contract.UsageFault(fmt.Sprintf("path %q %s", path, reason),
					contract.Remediation{Command: "igdev gateway api GET /data/api/v1/gateway-info", Why: "pass a path on this Instance's Gateway"})
			}
			extra, fault := apiHeaders(headers)
			if fault != nil {
				return fault
			}
			body, fault := a.apiBody(data)
			if fault != nil {
				return fault
			}
			g, err := a.gatewayContext()
			if err != nil {
				return err
			}
			var reader io.Reader
			if body != nil {
				reader = bytes.NewReader(body)
			}
			resp, target, err := sendAPI(g, method, path, reader, extra)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if output != "" && resp.StatusCode < 400 {
				saved, err := saveAPIResponse(resp, method, path, target, output)
				if err != nil {
					return err
				}
				a.emit(g.res, saved, func() {
					fmt.Fprintf(a.Stdout, "wrote %d bytes to %s\n", saved.BodyBytes, saved.Output)
				})
				return nil
			}
			if !g.res.IsJSON() && resp.StatusCode < 400 {
				// Human mode has no envelope to keep small: the whole body goes
				// to the terminal or the pipe.
				last := &lastByteWriter{w: a.Stdout}
				if _, err := io.Copy(last, resp.Body); err != nil {
					return contract.NewFault(contract.CodeGatewayAPI, contract.ExitFailure,
						fmt.Sprintf("%s %s answered %s, but the body could not be read: %v", method, path, resp.Status, err)).WithCause(err)
				}
				if last.n > 0 && last.last != '\n' {
					fmt.Fprintln(a.Stdout)
				}
				return nil
			}
			out := readAPIResponse(resp, method, path, target)
			if resp.StatusCode >= 400 {
				return contract.NewFault(contract.CodeGatewayAPI, contract.ExitFailure,
					fmt.Sprintf("%s %s answered %s", method, path, resp.Status)).
					WithData(out).
					WithRemediation(contract.Remediation{Command: "igdev gateway logs --tail 50", Why: "read why the Gateway refused it"})
			}
			a.emit(g.res, out, func() {})
			return nil
		},
	}
	cmd.Flags().StringVar(&data, "data", "", "request body: @file, - for stdin, or the body itself")
	cmd.Flags().StringArrayVar(&headers, "header", nil, "extra request header, Name: value (repeatable)")
	cmd.Flags().StringVar(&output, "output", "", "write the whole response body to this file instead of printing it (alone: the path's last segment)")
	cmd.Flags().Lookup("output").NoOptDefVal = outputFromPath
	return cmd
}

// outputFromPath is --output given without a value. pflag then leaves a
// space-separated file name among the arguments, so apiOutputFile takes it back.
const outputFromPath = "{basename}"

// apiOutputFile resolves a bare --output: with three arguments, the one that is
// not <METHOD> <path> is the file (--output openapi.json); otherwise the file
// is named after the path once it is known.
func apiOutputFile(args []string) (string, []string) {
	if len(args) != 3 {
		return outputFromPath, args
	}
	if validMethod(strings.ToUpper(args[0])) {
		return args[2], args[:2]
	}
	return args[0], args[1:]
}

// apiDefaultOutput is the file a bare --output writes: the path's last segment
// without its query, or response.body when the path has none.
func apiDefaultOutput(p string) string {
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	name := pathpkg.Base(p)
	if name == "" || name == "/" || name == "." || name == ".." {
		return "response.body"
	}
	return name
}

// sendAPI sends one request to the Instance's Gateway with its token, and
// returns the answer and the URL it went to. A Gateway that does not answer is
// IGDEV_E_GATEWAY_UNHEALTHY.
func sendAPI(g *gateway, method, path string, body io.Reader, extra http.Header) (*http.Response, string, error) {
	req, err := gatewayapi.NewRequest(method, g.url(), path, body, g.token, extra)
	if err != nil {
		return nil, "", contract.UsageFault(err.Error(),
			contract.Remediation{Command: "igdev help gateway api", Why: "show what gateway api accepts"})
	}
	// The timeout covers connecting and the answer's headers; a large body then
	// streams for as long as it takes.
	client := &http.Client{Transport: &http.Transport{
		Proxy:                 nil,
		ResponseHeaderTimeout: apiTimeout,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
	}}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", contract.NewFault(contract.CodeGatewayUnhealthy, contract.ExitFailure,
			fmt.Sprintf("the Gateway at %s did not answer %s %s (%v)", g.url(), method, path, err)).
			WithCause(err).
			WithRemediation(contract.Remediation{Command: "igdev gateway ensure", Why: "leave this Instance with a running Gateway"})
	}
	return resp, req.URL.String(), nil
}

// saveAPIResponse streams a successful answer's whole body into dest, replacing
// it in one rename once the body has arrived.
func saveAPIResponse(resp *http.Response, method, path, url, dest string) (gatewayAPIFileData, error) {
	out := gatewayAPIFileData{Method: method, Path: path, URL: url, Status: resp.StatusCode, Headers: apiHeadersOf(resp)}
	abs, err := filepath.Abs(dest)
	if err != nil {
		abs = dest
	}
	out.Output = abs
	written, err := atomicfile.Fill(abs, 0o644, 0o755, func(w io.Writer) (int64, error) {
		return io.Copy(w, resp.Body)
	})
	if err != nil {
		return out, contract.NewFault(contract.CodeGatewayAPI, contract.ExitFailure,
			fmt.Sprintf("%s %s answered %s, but the body could not be written to %s: %v", method, path, resp.Status, abs, err)).
			WithCause(err).
			WithRemediation(contract.Remediation{Command: "igdev gateway api " + method + " " + path + " --output <writable file>", Why: "write the body somewhere igdev can create a file"})
	}
	out.BodyBytes = written
	return out, nil
}

// apiHeadersOf picks the reported response headers that were present.
func apiHeadersOf(resp *http.Response) map[string]string {
	out := map[string]string{}
	for _, name := range apiResponseHeaders {
		if v := resp.Header.Get(name); v != "" {
			out[strings.ToLower(name)] = v
		}
	}
	return out
}

func validMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
}

// apiHeaders parses --header values, refusing the ones igdev sets itself.
func apiHeaders(raw []string) (http.Header, *contract.Fault) {
	out := http.Header{}
	for _, h := range raw {
		name, value, ok := strings.Cut(h, ":")
		name = strings.TrimSpace(name)
		if !ok || name == "" || strings.ContainsAny(name, " \t\r\n") {
			return nil, contract.UsageFault(fmt.Sprintf("--header %q is not Name: value", h),
				contract.Remediation{Command: "igdev help gateway api", Why: "pass headers as Name: value"})
		}
		if gatewayapi.Reserved(name) {
			return nil, contract.UsageFault(fmt.Sprintf("--header %s is set by igdev and cannot be replaced", name),
				contract.Remediation{Command: "igdev help gateway api", Why: "the token and origin headers always belong to this Instance"})
		}
		out.Add(name, strings.TrimSpace(value))
	}
	return out, nil
}

// apiBody reads --data: @file, - for stdin, or the literal body.
func (a *App) apiBody(data string) ([]byte, *contract.Fault) {
	if data == "" {
		return nil, nil
	}
	var src io.Reader
	switch {
	case data == "-":
		src = a.Stdin
	case strings.HasPrefix(data, "@"):
		f, err := os.Open(data[1:])
		if err != nil {
			return nil, contract.UsageFault(fmt.Sprintf("--data %s cannot be read: %v", data, err),
				contract.Remediation{Command: "igdev help gateway api", Why: "pass a readable file after @"})
		}
		defer func() { _ = f.Close() }()
		src = f
	default:
		return []byte(data), nil
	}
	body, err := io.ReadAll(io.LimitReader(src, apiRequestLimit+1))
	if err != nil {
		return nil, contract.UsageFault(fmt.Sprintf("--data %s cannot be read: %v", data, err),
			contract.Remediation{Command: "igdev help gateway api", Why: "pass a readable body"})
	}
	if len(body) > apiRequestLimit {
		return nil, contract.UsageFault(fmt.Sprintf("--data %s is over the %d-byte limit", data, apiRequestLimit),
			contract.Remediation{Command: "igdev help gateway api", Why: "send a smaller body"})
	}
	return body, nil
}

// lastByteWriter passes writes through and remembers the last byte, so human
// mode can end a body that has no trailing newline with one.
type lastByteWriter struct {
	w    io.Writer
	n    int64
	last byte
}

func (l *lastByteWriter) Write(p []byte) (int, error) {
	n, err := l.w.Write(p)
	if n > 0 {
		l.n += int64(n)
		l.last = p[n-1]
	}
	return n, err
}

// readAPIResponse reads the answer up to the cap, counts the rest, and decodes it
// for the report.
func readAPIResponse(resp *http.Response, method, path, url string) gatewayAPIData {
	out := gatewayAPIData{Method: method, Path: path, URL: url, Status: resp.StatusCode, Headers: apiHeadersOf(resp)}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, apiResponseLimit))
	rest, _ := io.Copy(io.Discard, resp.Body)
	out.BodyBytes = int64(len(raw)) + rest
	out.Truncated = rest > 0
	switch {
	case len(raw) == 0:
		out.Body = nil
	case !out.Truncated && json.Valid(raw):
		out.Body = json.RawMessage(raw)
	default:
		out.Body = string(raw)
	}
	return out
}
