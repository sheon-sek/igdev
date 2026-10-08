package itest

import (
	"os"
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/consent"
	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/testrig"
)

const machineRecord = "home/.config/igdev/accepted.toml"

// A person exports the record they accepted, and a runner with no record of its
// own sets up from that file without writing one.
func TestConsentFileCarriesAPersonsAcceptance(t *testing.T) {
	env := testrig.NewEnv(t)
	person := env.Project("person", testrig.MinimalContract)
	testrig.WantExit(t, env.RunIn(person, "setup", "--accept-eula"), contract.ExitOK)
	exported := env.Path("secret", "igdev-consent.toml")
	res := env.RunIn(person, "consent", "export", "--output", exported, "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	var data struct {
		Terms []string `json:"terms"`
	}
	testrig.DataOf(t, res.Stdout, &data)
	if strings.Join(data.Terms, ",") != consent.EULA.ID {
		t.Errorf("exported terms = %v, want the EULA", data.Terms)
	}
	if info, err := os.Stat(exported); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the export is missing or not 0600: %v", err)
	}

	// The runner: no machine record, only the exported file.
	if err := os.Remove(env.Path(machineRecord)); err != nil {
		t.Fatal(err)
	}
	runner := env.Project("runner", testrig.MinimalContract)
	withFile := []string{consent.FileEnv + "=" + exported}
	res = env.Run(testrig.Run{Dir: runner, Args: []string{"setup", "--json"}, Env: withFile})
	testrig.WantExit(t, res, contract.ExitOK)
	if _, err := os.Stat(env.Path(machineRecord)); !os.IsNotExist(err) {
		t.Errorf("setup under %s wrote a machine record: %v", consent.FileEnv, err)
	}

	// Nothing on the runner may add to the record.
	res = env.Run(testrig.Run{Dir: runner, Args: []string{"setup", "--accept-eula", "--json"}, Env: withFile})
	testrig.WantExit(t, res, contract.ExitUsage)
	if _, err := os.Stat(env.Path(machineRecord)); !os.IsNotExist(err) {
		t.Errorf("--accept-eula under %s wrote a machine record: %v", consent.FileEnv, err)
	}
}

// A file that misses the term, or whose entry does not prove an acceptance, is
// the human-required fault, naming the file and the export command.
func TestConsentFileRefusesAMissingOrStaleTerm(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := env.Project("repo", testrig.MinimalContract)
	for name, body := range map[string]string{
		"missing":     "",
		"no version":  "[ignition-eula]\naccepted_at = \"2026-01-01T00:00:00Z\"\n",
		"future":      "[ignition-eula]\naccepted_at = \"2999-01-01T00:00:00Z\"\ncli_version = \"0.9.0\"\n",
		"not a time":  "[ignition-eula]\naccepted_at = \"yesterday\"\ncli_version = \"0.9.0\"\n",
		"other terms": "[module-license]\naccepted_at = \"2026-01-01T00:00:00Z\"\ncli_version = \"0.9.0\"\n",
	} {
		file := env.Write("secret/"+strings.ReplaceAll(name, " ", "-")+".toml", body)
		// Starting a Gateway is what needs the EULA; setup alone no longer does.
		res := env.Run(testrig.Run{Dir: dir, Args: []string{"gateway", "up", "--json"}, Env: []string{consent.FileEnv + "=" + file}})
		testrig.WantExit(t, res, contract.ExitHumanAction)
		envelope := testrig.Envelope(t, res.Stdout)
		testrig.WantCode(t, envelope, contract.CodeConsentRequired)
		testrig.WantRemediation(t, envelope, "igdev consent export")
		if !strings.Contains(envelope.Message, file) {
			t.Errorf("%s: the refusal does not name the file: %q", name, envelope.Message)
		}
	}
	if _, err := os.Stat(env.Path(machineRecord)); !os.IsNotExist(err) {
		t.Errorf("a refused setup wrote a machine record: %v", err)
	}
}
