// Package trial reads and resets an Ignition Gateway's trial over the Gateway's
// own REST API (ADR 0008).
//
// Ignition 8.3 reports the trial at GET /data/api/v1/trial, which needs no
// credential, and resets it at POST /data/api/v1/trial, which needs an API token
// and succeeds only once the trial has expired: before that the Gateway answers
// 403 whatever the token. Resetting is therefore something that happens after
// expiry, never ahead of it, and the Gateway is neither restarted nor rebuilt.
package trial

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/sheon-sek/igdev/internal/apitoken"
)

// Path is the trial endpoint, relative to the Gateway URL.
const Path = "/data/api/v1/trial"

// bodyLimit bounds how much of an answer is read: the trial document is a few
// hundred bytes, and an error page is not worth more than its first lines.
const bodyLimit = 64 << 10

// State is the trial as igdev reports it.
type State struct {
	// LicenseMode is the Gateway's license mode: Trial, Activated, Free, or Invalid.
	LicenseMode string `json:"license_mode"`
	// SecondsLeft is how long the trial has left; zero once it expired.
	SecondsLeft int `json:"seconds_left"`
	// Expired is true once the trial ran out, which is when a reset is accepted.
	Expired bool `json:"expired"`
}

// gatewayState is the Gateway's answer, as Ignition 8.3 spells it.
type gatewayState struct {
	LicenseMode      string  `json:"licenseMode"`
	TrialSecondsLeft float64 `json:"trialSecondsLeft"`
	Expired          bool    `json:"expired"`
}

// Read asks the Gateway at baseURL for its trial state.
func Read(client *http.Client, baseURL string) (State, error) {
	resp, err := client.Get(strings.TrimRight(baseURL, "/") + Path)
	if err != nil {
		return State{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, bodyLimit))
	if err != nil {
		return State{}, fmt.Errorf("read the trial answer: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return State{}, fmt.Errorf("GET %s answered %s", Path, resp.Status)
	}
	var got gatewayState
	if err := json.Unmarshal(body, &got); err != nil {
		return State{}, fmt.Errorf("GET %s answered something other than the trial document: %w", Path, err)
	}
	left := int(got.TrialSecondsLeft)
	if left < 0 {
		left = 0
	}
	return State{LicenseMode: got.LicenseMode, SecondsLeft: left, Expired: got.Expired}, nil
}

// Reset asks the Gateway at baseURL to start a fresh trial, presenting token. It
// returns the HTTP status the Gateway answered with; a transport failure is an
// error. The request carries the Origin and Referer the Gateway's own web UI
// sends, because the Gateway checks where a state-changing request came from.
func Reset(client *http.Client, baseURL, token string) (int, error) {
	base := strings.TrimRight(baseURL, "/")
	req, err := http.NewRequest(http.MethodPost, base+Path, strings.NewReader(""))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", base)
	req.Header.Set("Referer", base+"/app/home")
	req.Header.Set(apitoken.Header, token)
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, bodyLimit))
	return resp.StatusCode, nil
}
