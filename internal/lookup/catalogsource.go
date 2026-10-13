package lookup

import (
	"archive/zip"
	"io"
	"path"
	"sort"
)

// The readers below feed the dev-only Core Catalog generator (cmd/igdev-catalog):
// they read the same image the function index reads, through the same container
// and bundle code, so a new Ignition version's catalog data comes from the image
// itself rather than from the docs.

// ModuleFile is one built-in module archive of an image: its module.xml id and
// the archive's file name.
type ModuleFile struct {
	ID       string
	Filename string
}

// ImageModules lists the module archives an image ships in user-lib/modules,
// sorted by id. An archive without a readable module.xml is skipped.
func ImageModules(image, scratch string) ([]ModuleFile, error) {
	id, remove, err := createContainer(image)
	if err != nil {
		return nil, err
	}
	defer remove()
	modules, err := copyOut(id, imageModules, func(r io.Reader) ([]ModuleFile, error) { return readModuleIDsTar(r, scratch) })
	if err != nil {
		return nil, err
	}
	sort.Slice(modules, func(i, j int) bool { return modules[i].ID < modules[j].ID })
	return modules, nil
}

// readModuleIDsTar reads the module.xml id of every module archive in a tar
// stream of user-lib/modules.
func readModuleIDsTar(r io.Reader, scratch string) ([]ModuleFile, error) {
	var out []ModuleFile
	err := eachTarFile(r, ".modl", func(name string, body io.Reader, size int64) error {
		found, err := withTempArchive(scratch, body, size, func(z *zip.Reader) ([]ModuleFile, error) {
			raw, ok := readEntry(z, "module.xml", maxXML)
			if !ok {
				return nil, nil
			}
			module, ok := parseModuleXML(raw)
			if !ok {
				return nil, nil
			}
			return []ModuleFile{{ID: module.ID, Filename: path.Base(name)}}, nil
		})
		out = append(out, found...)
		return err
	})
	return out, err
}

// ImageFunctions lists the functions an image's bundles document, placed the
// way the function index places them. known names the catalog's functions.
func ImageFunctions(image, scratch string, known []string) ([]Function, error) {
	bundles, err := ReadImage(image, scratch)
	if err != nil {
		return nil, err
	}
	return buildFunctions(bundles, known, false), nil
}
