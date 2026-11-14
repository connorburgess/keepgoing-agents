//go:build unix

package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/envvar"
)

// Doctor as a whole does not also say a registry is in use when the
// variable's two names disagree: none is.
func TestDoctorNamesNoRegistryWhenItsNamesDisagree(t *testing.T) {
	for _, v := range envvar.Set {
		t.Setenv(v.Name(), "")
		t.Setenv(v.Legacy(), "")
	}
	f := newFixture(t)
	t.Setenv(envvar.RegistryDir.Name(), filepath.Join(f.dir, "registry-new"))
	t.Setenv(envvar.RegistryDir.Legacy(), filepath.Join(f.dir, "registry-old"))
	_, out, _ := f.run("doctor", "--json")
	if strings.Contains(out, `"registry.directory"`) || !strings.Contains(out, `"environment.registry_dir"`) {
		t.Fatalf("%s", out)
	}
}
