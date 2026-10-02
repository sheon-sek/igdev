package runtimeassets

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path"

	"github.com/sheon-sek/igdev/internal/apitoken"
)

// SeedDir is the directory inside `.igdev/runtime/` that the Instance image copies
// over the Gateway's data directory. A fresh named volume takes the image's data
// directory as its initial content, so whatever the seed holds is in place before
// the Gateway's first start (ADR 0007).
const SeedDir = "seed"

// seedCollection is where the seed puts Gateway configuration: the `external`
// resource collection, which ships empty in the image and is the parent of the
// `core` collection the Gateway creates on its first start. `core` itself cannot be
// seeded — a Gateway whose core collection exists before commissioning faults with
// "Unable to create 'core' resource collection".
const seedCollection = "config/resources/external/ignition"

// Placeholders for the login names in the seeded security settings. The Gateway
// entrypoint wrapper replaces them on a volume's first start with the names that
// volume logs people in with, which igdev only knows at `gateway up` time
// (ADR 0007, amendment 1).
const (
	SystemUserSourcePlaceholder       = "__IGDEV_SYSTEM_USER_SOURCE__"
	SystemIdentityProviderPlaceholder = "__IGDEV_SYSTEM_IDENTITY_PROVIDER__"
)

// The levels a Gateway ships with, as the 8.3 commissioning writes them.
var (
	administratorPath = levelPath{Name: "Authenticated", Children: []levelPath{
		{Name: "Roles", Children: []levelPath{{Name: "Administrator", Children: []levelPath{}}}},
	}}
	igdevPath = levelPath{Name: "Authenticated", Children: []levelPath{
		{Name: apitoken.SecurityLevel, Children: []levelPath{}},
	}}
)

// levelPath names one security level by its path from the root, the shape
// security-properties and an API token profile reference levels with.
type levelPath struct {
	Name     string      `json:"name"`
	Children []levelPath `json:"children"`
}

// level is one node of the security-levels tree.
type level struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Children    []level `json:"children"`
}

type permission struct {
	Type           string      `json:"type"`
	SecurityLevels []levelPath `json:"securityLevels"`
}

// securityProperties mirrors the Gateway's general security settings as 8.3
// commissioning writes them, with the igdev level added next to Administrator
// wherever Administrator is required. The system user source and identity
// provider are placeholders the entrypoint wrapper fills in.
type securityProperties struct {
	AccessPermissions                  permission `json:"accessPermissions"`
	AllowDesignerSSO                   bool       `json:"allowDesignerSSO"`
	AllowUserAdmin                     bool       `json:"allowUserAdmin"`
	CreateProjectPermissions           permission `json:"createProjectPermissions"`
	DesignerAuthStrategy               string     `json:"designerAuthStrategy"`
	DesignerAuthTokenInactivityTimeout int        `json:"designerAuthTokenInactivityTimeout"`
	DesignerAuthTokenTimeToLive        int        `json:"designerAuthTokenTimeToLive"`
	DesignerPermissions                permission `json:"designerPermissions"`
	DesignerRoleName                   string     `json:"designerRoleName"`
	ForceIdpAuth                       bool       `json:"forceIdpAuth"`
	ReadPermissions                    permission `json:"readPermissions"`
	SystemAuthProfile                  string     `json:"systemAuthProfile"`
	SystemIdentityProvider             string     `json:"systemIdentityProvider"`
	UserInactivityTimeout              int        `json:"userInactivityTimeout"`
	WritePermissions                   permission `json:"writePermissions"`
}

type tokenConfig struct {
	Profile  tokenProfile  `json:"profile"`
	Settings tokenSettings `json:"settings"`
}

type tokenProfile struct {
	Type                  string  `json:"type"`
	SecureChannelRequired bool    `json:"secureChannelRequired"`
	SecurityLevels        []level `json:"securityLevels"`
	// Timestamp is when the token was created, in epoch milliseconds; the
	// Gateway refuses a token resource without it.
	Timestamp int64 `json:"timestamp"`
}

type tokenSettings struct {
	TokenHash string `json:"tokenHash"`
}

// resourceMeta is a resource's resource.json.
type resourceMeta struct {
	Scope       string     `json:"scope"`
	Version     int        `json:"version"`
	Restricted  bool       `json:"restricted"`
	Overridable bool       `json:"overridable"`
	Files       []string   `json:"files"`
	Attributes  attributes `json:"attributes"`
}

type attributes struct {
	UUID    string `json:"uuid"`
	Enabled bool   `json:"enabled"`
}

// seedFiles renders the Gateway configuration the Instance image carries: the
// igdev security level, the general security settings that grant it, and the
// Instance's API token, stored as its hash. Nothing here is a secret.
//
// security-properties is marked not overridable. The core collection the Gateway
// creates on its first start writes its own default security settings, which
// would otherwise shadow these and leave the token without rights; the cost is
// that a person cannot edit the general security settings of this development
// Gateway from its web UI (ADR 0007). Being authoritative, it must name the user
// source and identity provider that really exist, or every browser login fails
// (igdev#69); those names are placeholders here and are filled in at start.
func seedFiles(in Input) ([]File, error) {
	if in.APITokenHash == "" {
		return nil, nil
	}
	allowed := permission{Type: "AnyOf", SecurityLevels: []levelPath{administratorPath, igdevPath}}
	open := permission{Type: "AllOf", SecurityLevels: []levelPath{}}
	resources := []struct {
		name        string
		overridable bool
		config      any
	}{
		{"security-levels", true, map[string]any{"securityLevels": []level{{
			Name:        "Authenticated",
			Description: "Represents a user who has been authenticated by the system.",
			Children: []level{
				{Name: "Roles", Description: "Represents the roles that a user has.", Children: []level{{
					Name:        "Administrator",
					Description: "System generated security level representing read and write privileges to Gateway configuration",
					Children:    []level{},
				}}},
				{Name: apitoken.SecurityLevel, Description: "The API token igdev administers this development Gateway with.", Children: []level{}},
			},
		}}}},
		{"security-properties", false, securityProperties{
			AccessPermissions:                  open,
			CreateProjectPermissions:           open,
			DesignerAuthStrategy:               "CLASSIC",
			DesignerAuthTokenInactivityTimeout: 10,
			DesignerPermissions:                allowed,
			DesignerRoleName:                   "Administrator",
			ForceIdpAuth:                       true,
			ReadPermissions:                    allowed,
			SystemAuthProfile:                  SystemUserSourcePlaceholder,
			SystemIdentityProvider:             SystemIdentityProviderPlaceholder,
			UserInactivityTimeout:              10,
			WritePermissions:                   allowed,
		}},
		{"api-token/" + apitoken.Name, true, tokenConfig{
			Profile: tokenProfile{
				Type: "basic-token",
				SecurityLevels: []level{{Name: "Authenticated", Description: "", Children: []level{
					{Name: apitoken.SecurityLevel, Description: "", Children: []level{}},
				}}},
				Timestamp: in.CreatedAtMillis,
			},
			Settings: tokenSettings{TokenHash: in.APITokenHash},
		}},
	}
	out := make([]File, 0, 2*len(resources))
	for _, r := range resources {
		dir := path.Join(SeedDir, seedCollection, r.name)
		config, err := json.MarshalIndent(r.config, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("render seed %s: %w", r.name, err)
		}
		meta, err := json.MarshalIndent(resourceMeta{
			Scope:       "A",
			Version:     1,
			Overridable: r.overridable,
			Files:       []string{"config.json"},
			Attributes:  attributes{UUID: seedUUID(in.InstanceID, r.name), Enabled: true},
		}, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("render seed %s: %w", r.name, err)
		}
		out = append(out,
			File{Name: path.Join(dir, "config.json"), Data: append(config, '\n')},
			File{Name: path.Join(dir, "resource.json"), Data: append(meta, '\n')},
		)
	}
	return out, nil
}

// seedUUID derives a resource's uuid from the Instance identity and the resource
// name, so rendering stays a pure function of its input.
func seedUUID(instanceID, name string) string {
	sum := sha256.Sum256([]byte(instanceID + "\x00" + name))
	sum[6] = (sum[6] & 0x0f) | 0x50 // version 5: name-based
	sum[8] = (sum[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}
