package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/sheon-sek/igdev/internal/atomicfile"
	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/modules"
	"github.com/sheon-sek/igdev/internal/project"
)

// installedRecordName is the file in the module staging directory that records
// the digest of each module `build --install` last installed, by module id.
const installedRecordName = ".installed.json"

// runInstallStage is `build --install`'s module-install stage: every artifact the
// restage stage staged is hot-installed unless its bytes were installed last time.
func (a *App) runInstallStage(found project.Found, data *pipelineData, timeout time.Duration) *contract.Fault {
	stage := stageResult{Stage: "module-install", Installs: []moduleInstallResult{}}
	var artifacts []stagedArtifact
	for _, s := range data.Stages {
		if s.Stage == "module-restage" {
			artifacts = s.Artifacts
		}
	}
	if len(artifacts) == 0 {
		stage.Status, stage.Message = stageSkipped, "no [modules].artifacts declared"
		data.Stages = append(data.Stages, stage)
		return nil
	}
	fail := func(fault *contract.Fault) *contract.Fault {
		stage.Status, stage.Message = stageFailed, fault.Message
		data.Stages = append(data.Stages, stage)
		return failPipeline(data, stage.Stage, fault)
	}
	g, err := a.gatewayContext()
	if err != nil {
		return fail(contract.AsFault(err))
	}
	recordPath := filepath.Join(modules.Dir(found.Root), installedRecordName)
	installed := readInstalled(recordPath)
	for _, artifact := range artifacts {
		digest, err := fileDigest(artifact.Source)
		if err != nil {
			return fail(contract.NewFault(contract.CodeModuleArchiveInvalid, contract.ExitFailure,
				"cannot read "+artifact.Source+": "+err.Error()).WithCause(err))
		}
		result := moduleInstallResult{ID: artifact.ID, Artifact: artifact.Artifact}
		if installed[artifact.ID] == digest {
			result.Action = "unchanged"
			stage.Installs = append(stage.Installs, result)
			continue
		}
		out, err := a.installModule(g, artifact.Source, timeout)
		result.Action, result.Status, result.Restarted = "installed", out.Status, out.Restarted
		stage.Installs = append(stage.Installs, result)
		if err != nil {
			return fail(contract.AsFault(err))
		}
		installed[artifact.ID] = digest
		if err := writeInstalled(recordPath, installed); err != nil {
			return fail(contract.NewFault(contract.CodeInternal, contract.ExitFailure,
				"cannot record the installed digest in "+recordPath+": "+err.Error()).WithCause(err))
		}
	}
	stage.Status = stagePassed
	data.Stages = append(data.Stages, stage)
	return nil
}

// readInstalled reads the digest record; a missing or unreadable record means
// nothing is known to be installed, so every artifact is installed.
func readInstalled(path string) map[string]string {
	out := map[string]string{}
	raw, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	_ = json.Unmarshal(raw, &out)
	return out
}

func writeInstalled(path string, installed map[string]string) error {
	raw, err := json.MarshalIndent(installed, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(path, append(raw, '\n'), 0o644, 0o755)
}

// fileDigest is the sha256 of a file, hex encoded.
func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
