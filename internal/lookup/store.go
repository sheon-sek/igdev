package lookup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"github.com/sheon-sek/igdev/internal/atomicfile"
)

// FormatVersion is the shape of the cached index files. A file of another
// format is rebuilt, never read.
const FormatVersion = 1

// FunctionIndex is the cached function index of one image.
type FunctionIndex struct {
	Format    int        `json:"format"`
	Version   string     `json:"version"`
	Image     string     `json:"image"`
	ImageID   string     `json:"image_id"`
	Functions []Function `json:"functions"`
}

// ModuleIndex is the cached function index of one private module archive,
// keyed by the archive's sha256 and the catalog that placed its bundles.
type ModuleIndex struct {
	Format    int        `json:"format"`
	Key       string     `json:"key"`
	SHA256    string     `json:"sha256"`
	Artifact  string     `json:"artifact"`
	Functions []Function `json:"functions"`
}

// RESTIndex is the cached REST index of one Ignition version.
type RESTIndex struct {
	Format  int    `json:"format"`
	Version string `json:"version"`
	// Source is SourceOpenAPI or SourceEmbedded.
	Source string `json:"source"`
	// SHA256 is the OpenAPI document's digest; empty for the embedded fallback.
	SHA256    string     `json:"sha256,omitempty"`
	Endpoints []Endpoint `json:"endpoints"`
}

// Store is the lookup cache of one Ignition version:
// <cache>/lookup/<version>/{functions.json, rest.json, openapi.json, modules/}.
type Store struct{ Dir string }

// NewStore is the store under igdev's cache directory.
func NewStore(cacheDir, version string) Store {
	return Store{Dir: filepath.Join(cacheDir, "lookup", version)}
}

// FunctionsPath, RESTPath and OpenAPIPath are the store's files.
func (s Store) FunctionsPath() string { return filepath.Join(s.Dir, "functions.json") }
func (s Store) RESTPath() string      { return filepath.Join(s.Dir, "rest.json") }
func (s Store) OpenAPIPath() string   { return filepath.Join(s.Dir, "openapi.json") }

// ModulePath is where one private module's index is cached.
func (s Store) ModulePath(key string) string {
	return filepath.Join(s.Dir, "modules", key+".json")
}

// ModuleKey keys a private module's index: the archive's digest and a digest of
// the catalog function names that placed its bundles.
func ModuleKey(sha string, known []string) string {
	h := sha256.New()
	for _, name := range known {
		h.Write([]byte(name))
		h.Write([]byte{'\n'})
	}
	return sha[:min(len(sha), 32)] + "-" + hex.EncodeToString(h.Sum(nil))[:12]
}

// LoadFunctions reads the cached function index, or reports there is none.
func (s Store) LoadFunctions() (*FunctionIndex, bool) {
	var idx FunctionIndex
	if !load(s.FunctionsPath(), &idx) || idx.Format != FormatVersion {
		return nil, false
	}
	return &idx, true
}

// SaveFunctions writes the function index in one rename.
func (s Store) SaveFunctions(idx *FunctionIndex) error {
	idx.Format = FormatVersion
	return save(s.FunctionsPath(), idx)
}

// LoadModule reads a cached private module index.
func (s Store) LoadModule(key string) (*ModuleIndex, bool) {
	var idx ModuleIndex
	if !load(s.ModulePath(key), &idx) || idx.Format != FormatVersion || idx.Key != key {
		return nil, false
	}
	return &idx, true
}

// SaveModule writes a private module index in one rename.
func (s Store) SaveModule(idx *ModuleIndex) error {
	idx.Format = FormatVersion
	return save(s.ModulePath(idx.Key), idx)
}

// LoadREST reads the cached REST index.
func (s Store) LoadREST() (*RESTIndex, bool) {
	var idx RESTIndex
	if !load(s.RESTPath(), &idx) || idx.Format != FormatVersion {
		return nil, false
	}
	return &idx, true
}

// SaveREST writes the REST index in one rename.
func (s Store) SaveREST(idx *RESTIndex) error {
	idx.Format = FormatVersion
	return save(s.RESTPath(), idx)
}

func load(path string, into any) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return json.Unmarshal(raw, into) == nil
}

func save(path string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return atomicfile.Write(path, append(raw, '\n'), 0o644, 0o755)
}

// FileSHA256 is the hex sha256 of a file's content.
func FileSHA256(path string) (string, error) {
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

// BuildImageIndex reads an image's bundles into a function index. known names
// the catalog's functions, which place the bundles the table does not.
func BuildImageIndex(version, image, imageID string, known []string, scratch string) (*FunctionIndex, error) {
	bundles, err := ReadImage(image, scratch)
	if err != nil {
		return nil, err
	}
	return &FunctionIndex{
		Format:    FormatVersion,
		Version:   version,
		Image:     image,
		ImageID:   imageID,
		Functions: buildFunctions(bundles, known, false),
	}, nil
}

// BuildModuleIndex reads one private module archive's bundles. A bundle no
// table or catalog row places is kept under its class name.
func BuildModuleIndex(file, sha, key string, known []string, scratch string) (*ModuleIndex, error) {
	bundles, err := readModuleFile(file, scratch)
	if err != nil {
		return nil, err
	}
	return &ModuleIndex{
		Format:    FormatVersion,
		Key:       key,
		SHA256:    sha,
		Artifact:  filepath.Base(file),
		Functions: buildFunctions(bundles, known, true),
	}, nil
}
