package control

import "testing"

func TestMissingFileIsDisabled(t *testing.T) {
	f, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if f.Enabled {
		t.Error("missing control file must mean disabled")
	}
}

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if _, err := Update(dir, func(f *File) { f.Enabled = true; f.Paused = true; f.Note = "lunch" }); err != nil {
		t.Fatal(err)
	}
	f, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !f.Enabled || !f.Paused || f.Note != "lunch" || f.UpdatedAt.IsZero() {
		t.Errorf("unexpected %+v", f)
	}
	if f.Summary() != "paused" {
		t.Errorf("summary = %q", f.Summary())
	}
	g := f.Gate()
	if !g.Enabled || !g.Paused {
		t.Errorf("gate = %+v", g)
	}
}
