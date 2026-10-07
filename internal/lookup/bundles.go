package lookup

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path"
	"sort"
	"strings"
)

// The bounds on what the bundle reader opens: a jar or module archive larger
// than maxArchive is skipped rather than read, and a properties entry larger than
// maxBundle is not a documentation bundle.
const (
	maxArchive = 512 << 20
	maxBundle  = 4 << 20
	maxXML     = 1 << 20
)

// Scope names, in the order a function's scopes are reported.
const (
	ScopeGateway  = "gateway"
	ScopeClient   = "client"
	ScopeDesigner = "designer"
)

// bundle is one scripting documentation bundle: a properties resource whose keys
// are `<function>.desc`, `<function>.param.<name>` and `<function>.returns`.
type bundle struct {
	// Class is the bundle's simple name, AbstractTagUtilities for
	// com/inductiveautomation/.../AbstractTagUtilities.properties.
	Class string
	// Source is where the bundle was read: the jar, inside its module archive
	// when it has one, then the resource path.
	Source string
	// Module is the id of the module archive the jar came from; empty for a
	// platform jar.
	Module string
	// Scopes are the scopes the jar is loaded in.
	Scopes []string
	props  *properties
}

// functions lists the function names the bundle documents, in file order.
func (b bundle) functions() []string {
	var out []string
	for _, key := range b.props.keys {
		if name, ok := strings.CutSuffix(key, ".desc"); ok && validIdentifier(name) {
			out = append(out, name)
		}
	}
	return out
}

// scripting reports whether the bundle documents functions: at least one of its
// entries names a parameter or a return value.
func (b bundle) scripting() bool {
	for _, key := range b.props.keys {
		if strings.Contains(key, ".param.") || strings.HasSuffix(key, ".returns") {
			return true
		}
	}
	return false
}

// validIdentifier reports whether name is a bare function name, not a dotted key.
func validIdentifier(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r == '_', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// coreScopes maps a lib/core subdirectory to the scopes its jars load in.
func coreScopes(dir string) []string {
	switch dir {
	case "common":
		return []string{ScopeGateway, ScopeClient, ScopeDesigner}
	case "gateway":
		return []string{ScopeGateway}
	case "client":
		return []string{ScopeClient, ScopeDesigner}
	case "designer":
		return []string{ScopeDesigner}
	}
	return nil
}

// letterScopes maps a module.xml jar scope such as "CDG" to scope names.
func letterScopes(letters string) []string {
	var out []string
	for _, pair := range []struct {
		letter byte
		scope  string
	}{{'G', ScopeGateway}, {'C', ScopeClient}, {'D', ScopeDesigner}} {
		if strings.IndexByte(strings.ToUpper(letters), pair.letter) >= 0 {
			out = append(out, pair.scope)
		}
	}
	return out
}

// readCoreTar reads the bundles of every jar in a tar stream of the image's
// lib/core directory, as `docker cp <id>:<dir> -` writes it. The scope of a jar
// is the subdirectory it sits in.
func readCoreTar(r io.Reader, scratch string) ([]bundle, error) {
	var out []bundle
	err := eachTarFile(r, ".jar", func(name string, body io.Reader, size int64) error {
		parts := strings.Split(path.Clean(name), "/")
		var scopes []string
		for _, part := range parts {
			if scopes = coreScopes(part); scopes != nil {
				break
			}
		}
		if scopes == nil {
			return nil
		}
		found, err := withTempArchive(scratch, body, size, func(z *zip.Reader) ([]bundle, error) {
			return readJar(z, "lib/core/"+strings.Join(parts[1:], "/"), "", scopes)
		})
		out = append(out, found...)
		return err
	})
	return out, err
}

// readModulesTar reads the bundles of every module archive in a tar stream of
// the image's user-lib/modules directory.
func readModulesTar(r io.Reader, scratch string) ([]bundle, error) {
	var out []bundle
	err := eachTarFile(r, ".modl", func(name string, body io.Reader, size int64) error {
		found, err := withTempArchive(scratch, body, size, func(z *zip.Reader) ([]bundle, error) {
			return readModule(z, path.Base(name), scratch)
		})
		out = append(out, found...)
		return err
	})
	return out, err
}

// readModuleFile reads the bundles of one module archive on disk: a private
// module this checkout stages.
func readModuleFile(file, scratch string) ([]bundle, error) {
	z, err := zip.OpenReader(file)
	if err != nil {
		return nil, err
	}
	defer z.Close()
	return readModule(&z.Reader, path.Base(file), scratch)
}

// eachTarFile calls fn for every regular file in a tar stream whose name ends
// in suffix and whose size is inside the archive bound.
func eachTarFile(r io.Reader, suffix string, fn func(name string, body io.Reader, size int64) error) error {
	tr := tar.NewReader(r)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if header.Typeflag != tar.TypeReg || !strings.HasSuffix(strings.ToLower(header.Name), suffix) || header.Size > maxArchive {
			continue
		}
		if err := fn(header.Name, tr, header.Size); err != nil {
			return err
		}
	}
}

// withTempArchive spills one archive to a scratch file so it can be opened as a
// zip without holding it in memory, then removes it.
func withTempArchive(scratch string, body io.Reader, size int64, fn func(*zip.Reader) ([]bundle, error)) ([]bundle, error) {
	tmp, err := os.CreateTemp(scratch, "archive-*")
	if err != nil {
		return nil, err
	}
	defer func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}()
	if _, err := io.Copy(tmp, io.LimitReader(body, size)); err != nil {
		return nil, err
	}
	z, err := zip.NewReader(tmp, size)
	if err != nil {
		// Not a zip: nothing to index, and not a reason to fail the whole build.
		return nil, nil
	}
	return fn(z)
}

// moduleDoc is the part of module.xml the reader needs: the id and which scopes
// each jar loads in.
type moduleDoc struct {
	ID   string `xml:"id"`
	Jars []struct {
		Scope string `xml:"scope,attr"`
		Name  string `xml:",chardata"`
	} `xml:"jar"`
}

// parseModuleXML reads module.xml under the <modules> wrapper Ignition writes or
// as a bare <module> root.
func parseModuleXML(raw []byte) (moduleDoc, bool) {
	var wrapped struct {
		Modules []moduleDoc `xml:"module"`
	}
	if xml.Unmarshal(raw, &wrapped) == nil && len(wrapped.Modules) > 0 && wrapped.Modules[0].ID != "" {
		return wrapped.Modules[0], true
	}
	var bare moduleDoc
	if xml.Unmarshal(raw, &bare) == nil && bare.ID != "" {
		return bare, true
	}
	return moduleDoc{}, false
}

// readModule reads the bundles of every jar a module archive declares, each in
// the scopes module.xml gives it.
func readModule(z *zip.Reader, archive, scratch string) ([]bundle, error) {
	raw, ok := readEntry(z, "module.xml", maxXML)
	if !ok {
		return nil, nil
	}
	module, ok := parseModuleXML(raw)
	if !ok {
		return nil, nil
	}
	scopes := map[string][]string{}
	for _, jar := range module.Jars {
		scopes[strings.TrimSpace(jar.Name)] = letterScopes(jar.Scope)
	}
	var out []bundle
	for _, file := range z.File {
		name := path.Base(file.Name)
		jarScopes, declared := scopes[name]
		if !declared || file.UncompressedSize64 > maxArchive {
			continue
		}
		handle, err := file.Open()
		if err != nil {
			continue
		}
		found, err := withTempArchive(scratch, handle, int64(file.UncompressedSize64), func(jar *zip.Reader) ([]bundle, error) {
			return readJar(jar, archive+"!/"+name, module.ID, jarScopes)
		})
		handle.Close()
		if err != nil {
			return out, err
		}
		out = append(out, found...)
	}
	return out, nil
}

// readJar reads every documentation bundle in one jar. A localized variant
// (Name_de.properties) is skipped: the index carries the base language.
func readJar(z *zip.Reader, source, module string, scopes []string) ([]bundle, error) {
	var out []bundle
	for _, file := range z.File {
		if !strings.HasSuffix(file.Name, ".properties") || file.UncompressedSize64 > maxBundle {
			continue
		}
		class := strings.TrimSuffix(path.Base(file.Name), ".properties")
		if localized(class) {
			continue
		}
		raw, ok := readZipFile(file, maxBundle)
		if !ok || !bytes.Contains(raw, []byte(".desc")) {
			continue
		}
		b := bundle{Class: class, Source: source + "!/" + file.Name, Module: module, Scopes: scopes, props: parseProperties(raw)}
		if len(b.functions()) == 0 {
			continue
		}
		out = append(out, b)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	return out, nil
}

// localized reports whether a bundle name carries a locale suffix such as _de
// or _zh_CN.
func localized(class string) bool {
	i := strings.Index(class, "_")
	if i <= 0 {
		return false
	}
	suffix := class[i+1:]
	lang, _, _ := strings.Cut(suffix, "_")
	if len(lang) != 2 {
		return false
	}
	return lang == strings.ToLower(lang)
}

// readEntry reads one named zip entry under a size bound.
func readEntry(z *zip.Reader, name string, limit int64) ([]byte, bool) {
	for _, file := range z.File {
		if file.Name == name {
			return readZipFile(file, limit)
		}
	}
	return nil, false
}

func readZipFile(file *zip.File, limit int64) ([]byte, bool) {
	handle, err := file.Open()
	if err != nil {
		return nil, false
	}
	defer handle.Close()
	raw, err := io.ReadAll(io.LimitReader(handle, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, false
	}
	return raw, true
}
