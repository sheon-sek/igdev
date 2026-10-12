package lookup

import (
	"bytes"
	"testing"
)

func TestModuleIDsReadFromModuleXML(t *testing.T) {
	stream := tarOf(t, map[string][]byte{
		"modules/Tag Historian-module.modl": zipOf(t, map[string][]byte{
			"module.xml": []byte(`<modules><module><id>com.inductiveautomation.historian</id></module></modules>`),
		}),
		"modules/Bare-module.modl":  zipOf(t, map[string][]byte{"module.xml": []byte(`<module><id>com.example.bare</id></module>`)}),
		"modules/NoXML-module.modl": zipOf(t, map[string][]byte{"other.txt": []byte("x")}),
		"modules/readme.txt":        []byte("not a module"),
	})
	got, err := readModuleIDsTar(bytes.NewReader(stream), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"com.inductiveautomation.historian": "Tag Historian-module.modl", "com.example.bare": "Bare-module.modl"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for _, m := range got {
		if want[m.ID] != m.Filename {
			t.Errorf("module %s = %q, want %q", m.ID, m.Filename, want[m.ID])
		}
	}
}
