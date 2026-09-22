// Package modules reads the private module artifacts a checkout stages and
// answers the module questions the capability layer asks of a checkout.
//
// A `.modl` is a zip whose `module.xml` carries id, name, and version. That is
// all igdev reads from it: ADR 0005 makes the archive a fact about what is
// installed, never a source of capability knowledge.
package modules

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/project"
)

// DirName is the checkout-local directory private `.modl` artifacts live in,
// inside the Checkout Setup.
const DirName = "modules"

// Local is the source a checkout-local artifact reports.
const Local = "local"

// SuiteSelectorPrefix is the solution-suite selector module id prefix. Such an id
// is built-in without a catalog row, because the image resolves it at runtime.
const SuiteSelectorPrefix = "com.inductiveautomation.suite."

// Status values a private module record reports, matching the vocabulary the
// bash specification printed.
const (
	// StatusEnabled means the whitelist selects the module.
	StatusEnabled = "enabled"
	// StatusStagedNotEnabled means the artifact is staged but the whitelist does
	// not select it.
	StatusStagedNotEnabled = "staged-not-enabled"
	// StatusUnreadable means the artifact is present but its module.xml could
	// not be read.
	StatusUnreadable = "UNREADABLE"
	// StatusMissingArtifact means the whitelist names the module and no artifact
	// declares it.
	StatusMissingArtifact = "MISSING-ARTIFACT"
)

// Record is one private module artifact found in the checkout.
type Record struct {
	// ID is the module id from module.xml; empty when the artifact could not be
	// read.
	ID string
	// Name and Version come from module.xml.
	Name    string
	Version string
	// Artifact is the file name, which is all that identifies an unreadable
	// artifact.
	Artifact string
	// Source names where the artifact came from.
	Source string
	// Err is why module.xml could not be read; empty for a readable artifact.
	Err string
}

// Dir is the private module directory of a Project Root.
func Dir(root string) string {
	return filepath.Join(root, project.StateDir, DirName)
}

// Scan reads every `.modl` in dir, in file-name order. A missing directory is an
// empty result: no artifact is staged, which is the normal state before
// `igdev module add`.
//
// Artifacts that declare the same module id are collapsed: the last file wins and
// keeps the first file's position, exactly as the bash specification's
// private_records deduped them.
func Scan(dir string) ([]Record, *contract.Fault) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, contract.NewFault(contract.CodeInternal, contract.ExitFailure,
			"cannot read the private module directory "+dir+": "+err.Error()).WithCause(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".modl") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)

	var order []string
	byID := map[string]Record{}
	var out []Record
	for _, name := range names {
		record := read(filepath.Join(dir, name), name)
		if record.ID == "" {
			// An unreadable artifact has no id to collapse on; report it as it
			// is found.
			out = append(out, record)
			continue
		}
		if _, seen := byID[record.ID]; !seen {
			order = append(order, record.ID)
		}
		byID[record.ID] = record
	}
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out, nil
}

// read reads one `.modl`'s metadata.
func read(path, name string) Record {
	record := Record{Artifact: name, Source: Local}
	reader, err := zip.OpenReader(path)
	if err != nil {
		record.Err = err.Error()
		return record
	}
	defer reader.Close()
	for _, file := range reader.File {
		if file.Name != "module.xml" {
			continue
		}
		handle, err := file.Open()
		if err != nil {
			record.Err = err.Error()
			return record
		}
		raw, readErr := io.ReadAll(handle)
		handle.Close()
		if readErr != nil {
			record.Err = readErr.Error()
			return record
		}
		id, moduleName, version, err := metadata(raw)
		if err != nil {
			record.Err = err.Error()
			return record
		}
		record.ID, record.Name, record.Version = id, moduleName, version
		return record
	}
	record.Err = "the archive carries no module.xml"
	return record
}

// metadata reads id, name, and version out of a module.xml document. The tags
// are matched at any depth, so both the `<modules><module>` wrapper Ignition
// writes and a bare `<module>` root are read; the first occurrence of each tag
// wins, which is the element that identifies the module being installed.
func metadata(raw []byte) (id, name, version string, err error) {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	var stack []string
	for {
		token, tokenErr := decoder.Token()
		if tokenErr == io.EOF {
			break
		}
		if tokenErr != nil {
			return "", "", "", tokenErr
		}
		switch element := token.(type) {
		case xml.StartElement:
			stack = append(stack, element.Name.Local)
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			text := strings.TrimSpace(string(element))
			if text == "" || len(stack) == 0 {
				continue
			}
			switch stack[len(stack)-1] {
			case "id":
				if id == "" {
					id = text
				}
			case "name":
				if name == "" {
					name = text
				}
			case "version":
				if version == "" {
					version = text
				}
			}
		}
	}
	if id == "" {
		return "", "", "", errNoID
	}
	if name == "" {
		name = "<unknown>"
	}
	if version == "" {
		version = "<unknown>"
	}
	return id, name, version, nil
}

// errNoID is the failure of a module.xml that carries no usable id.
var errNoID = errString("module.xml carries no <id>")

// errString is a fixed error message.
type errString string

func (e errString) Error() string { return string(e) }

// Enabled reports whether the module whitelist selects a module id. The semantics
// come from the bash specification: an empty whitelist is no whitelist at all and
// enables every module.
func Enabled(whitelist []string, id string) bool {
	if len(whitelist) == 0 {
		return true
	}
	for _, candidate := range whitelist {
		if strings.TrimSpace(candidate) == id {
			return true
		}
	}
	return false
}

// Builtin reports whether a module id ships in the Ignition image: either the
// catalog knows it or it is a solution-suite selector.
func Builtin(inCatalog func(string) bool, id string) bool {
	return inCatalog(id) || strings.HasPrefix(id, SuiteSelectorPrefix)
}

// Has reports whether a readable artifact declares a module id.
func Has(records []Record, id string) bool {
	for _, record := range records {
		if record.ID == id {
			return true
		}
	}
	return false
}

// Status is a record's status under the whitelist.
func Status(record Record, whitelist []string) string {
	switch {
	case record.ID == "":
		return StatusUnreadable
	case Enabled(whitelist, record.ID):
		return StatusEnabled
	default:
		return StatusStagedNotEnabled
	}
}

// Missing lists the whitelisted module ids that are neither built-in nor declared
// by an artifact: the modules this environment would refuse to load.
func Missing(whitelist []string, builtin func(string) bool, records []Record) []string {
	var out []string
	for _, raw := range whitelist {
		id := strings.TrimSpace(raw)
		if id == "" || builtin(id) || Has(records, id) {
			continue
		}
		out = append(out, id)
	}
	return out
}
