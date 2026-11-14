package envvar

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func set(t *testing.T, v Var, current, earlier string) {
	t.Helper()
	t.Setenv(v.Name(), current)
	t.Setenv(v.Legacy(), earlier)
}

func TestNames(t *testing.T) {
	if got := SessionDir.Name(); got != "LOOPTENDER_SESSION_DIR" {
		t.Fatalf("Name = %q", got)
	}
	if got := SessionDir.Legacy(); got != "KEEPGOING_SESSION_DIR" {
		t.Fatalf("Legacy = %q", got)
	}
}

func TestLookup(t *testing.T) {
	for _, tc := range []struct {
		name             string
		current, earlier string
		value, from      string
		disagrees        bool
	}{
		{"neither set", "", "", "", "", false},
		{"only the current name", "new", "", "new", "LOOPTENDER_NET_PROBE", false},
		{"only the earlier name", "", "old", "old", "KEEPGOING_NET_PROBE", false},
		{"both, the same", "same", "same", "same", "LOOPTENDER_NET_PROBE", false},
		{"both, different: the current name wins", "new", "old", "new", "LOOPTENDER_NET_PROBE", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set(t, NetProbe, tc.current, tc.earlier)
			value, from := NetProbe.Lookup()
			if value != tc.value || from != tc.from {
				t.Fatalf("Lookup = %q from %q, want %q from %q", value, from, tc.value, tc.from)
			}
			if got := NetProbe.Get(); got != tc.value {
				t.Fatalf("Get = %q, want %q", got, tc.value)
			}
			if got := NetProbe.Disagrees(); got != tc.disagrees {
				t.Fatalf("Disagrees = %t, want %t", got, tc.disagrees)
			}
		})
	}
}

func TestPath(t *testing.T) {
	for _, tc := range []struct {
		name             string
		current, earlier string
		value, from      string
		refused          bool
	}{
		{"neither set", "", "", "", "", false},
		{"only the current name", "/private/example/new", "", "/private/example/new", "LOOPTENDER_REGISTRY_DIR", false},
		{"only the earlier name", "", "/private/example/old", "/private/example/old", "KEEPGOING_REGISTRY_DIR", false},
		{"both, the same place", "/private/example/one", "/private/example/one", "/private/example/one", "LOOPTENDER_REGISTRY_DIR", false},
		{"both, the same place written differently", "/private/example/one", "/private/example/one/", "/private/example/one", "LOOPTENDER_REGISTRY_DIR", false},
		{"both, different places", "/private/example/new", "/private/example/old", "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set(t, RegistryDir, tc.current, tc.earlier)
			value, from, err := RegistryDir.Path()
			if tc.refused {
				if !errors.Is(err, ErrDisagree) || value != "" || from != "" {
					t.Fatalf("Path = %q from %q, %v; want a refusal and no value", value, from, err)
				}
				// The refusal names both variables and neither place.
				for _, want := range []string{"LOOPTENDER_REGISTRY_DIR", "KEEPGOING_REGISTRY_DIR"} {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("refusal %q does not name %s", err, want)
					}
				}
				if strings.Contains(err.Error(), "/private/example") {
					t.Fatalf("refusal %q names a place", err)
				}
				return
			}
			if err != nil || value != tc.value || from != tc.from {
				t.Fatalf("Path = %q from %q, %v; want %q from %q", value, from, err, tc.value, tc.from)
			}
		})
	}
}

func TestForACommand(t *testing.T) {
	if got, want := Mode.Pairs("wake"), []string{"LOOPTENDER_MODE=wake", "KEEPGOING_MODE=wake"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Pairs = %v, want %v", got, want)
	}
	got := Mode.Both(map[string]string{"OTHER": "kept"}, "nudge")
	want := map[string]string{"OTHER": "kept", "LOOPTENDER_MODE": "nudge", "KEEPGOING_MODE": "nudge"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Both = %v, want %v", got, want)
	}
	if got := Mode.Both(nil, "handoff"); len(got) != 2 {
		t.Fatalf("Both(nil) = %v", got)
	}
}

func TestLegacyOf(t *testing.T) {
	for name, want := range map[string]string{
		"LOOPTENDER_WEBHOOK_URL":    "KEEPGOING_WEBHOOK_URL",
		"LOOPTENDER_WEBHOOK_SECRET": "KEEPGOING_WEBHOOK_SECRET",
		"KEEPGOING_WEBHOOK_URL":     "", // an earlier name has no earlier name
		"EXAMPLE_WEBHOOK_URL":       "", // a name the configuration chose
		"LOOPTENDER_UNKNOWN":        "",
		"LOOPTENDER_MODE":           "", // set by the tool, never read
		"":                          "",
	} {
		got, ok := LegacyOf(name)
		if got != want || ok != (want != "") {
			t.Errorf("LegacyOf(%q) = %q, %t; want %q", name, got, ok, want)
		}
	}
}

// Every location is also in the list doctor reports.
func TestLocationsAreSet(t *testing.T) {
	for _, l := range Locations {
		found := false
		for _, v := range Set {
			found = found || v == l
		}
		if !found {
			t.Errorf("%s is a location but not in Set", l)
		}
	}
}
