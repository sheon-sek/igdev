// Package agentskill holds the Agent Skill igdev installs: the thin workflow
// document bundled with the binary and written by `igdev agent skill-install`
// (into the global skills directory by default, or into a repository with
// --scope repo).
//
// SKILL.md is the entry an agent loads first. It teaches ordering, boundaries,
// and error handling, and routes to references/, the generated command and
// error-code reference an agent reads only when a task needs it. cmd/igdev-docs
// writes references/; nothing there is handwritten. The skill carries no API
// catalogs (the Core Catalog embedded in the binary is the machine source for
// those) and, apart from the CLI Contract Version in its frontmatter, no
// version-like detail. It is embedded with //go:embed, so installing it reads no
// repository file and no network.
package agentskill

import (
	"embed"
	"io/fs"

	"github.com/sheon-sek/igdev/internal/contract"
)

//go:embed SKILL.md references
var files embed.FS

const (
	// Name is the skill's name: the frontmatter name and the directory it
	// installs into.
	Name = "igdev"
	// Dir is the skill's directory name under a skills root.
	Dir = Name
	// FileName is the skill document's file name.
	FileName = "SKILL.md"
	// ReferencesDir is the directory beside SKILL.md that holds the reference
	// pages.
	ReferencesDir = "references"
)

// Version is the CLI Contract Version the installed skill's frontmatter must
// carry: the epoch of the machine interface the skill teaches. It is the running
// binary's contract version, and the package test keeps the embedded asset in
// step with it.
func Version() string { return contract.Version }

// Content returns the embedded SKILL.md bytes. Installation writes exactly these
// bytes, so an unchanged binary reinstalls byte-identical content and a run over
// an up-to-date install writes nothing.
func Content() []byte {
	raw, err := files.ReadFile(FileName)
	if err != nil {
		// The document is embedded at build time; a missing asset is a build
		// defect, not a runtime condition the caller could recover from.
		panic("agentskill: embedded " + FileName + " is missing: " + err.Error())
	}
	return raw
}

// Files returns every file the skill installs, keyed by its slash-separated
// path relative to the skill directory: SKILL.md and each references/ page.
func Files() map[string][]byte {
	out := map[string][]byte{}
	err := fs.WalkDir(files, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		raw, err := files.ReadFile(path)
		if err != nil {
			return err
		}
		out[path] = raw
		return nil
	})
	if err != nil {
		// The files are embedded at build time; a read failure is a build defect.
		panic("agentskill: reading the embedded skill: " + err.Error())
	}
	return out
}
