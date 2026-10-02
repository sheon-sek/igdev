package trial

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReadParsesTheGatewayDocument(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != Path || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"licenseMode":"Trial","trialState":"NoneInDemo","trialSecondsLeft":6931.6,` +
			`"expired":false,"emergency":false,"emergencySecondsLeft":0,"development":false,"developmentSecondsLeft":0}`))
	}))
	defer server.Close()
	got, err := Read(server.Client(), server.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	want := State{LicenseMode: "Trial", SecondsLeft: 6931, Expired: false}
	if got != want {
		t.Fatalf("Read = %+v, want %+v", got, want)
	}
}

func TestReadRefusesAnErrorAnswer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	if _, err := Read(server.Client(), server.URL); err == nil {
		t.Fatal("Read accepted a 503")
	}
}

// The reset is a POST with the token and the headers the Gateway's web UI sends.
func TestResetPresentsTheTokenAndOrigin(t *testing.T) {
	var got *http.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	status, err := Reset(server.Client(), server.URL, "igdev:key")
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	for header, want := range map[string]string{
		"X-Ignition-API-Token": "igdev:key",
		"Origin":               server.URL,
		"Referer":              server.URL + "/app/home",
		"Accept":               "application/json",
	} {
		if got.Header.Get(header) != want {
			t.Errorf("%s = %q, want %q", header, got.Header.Get(header), want)
		}
	}
	if got.Method != http.MethodPost || got.URL.Path != Path {
		t.Errorf("request = %s %s", got.Method, got.URL.Path)
	}
}
