package daemoninfo

import (
	"context"
	"testing"
	"time"
)

func TestConfigurationTimestampRoundTripsTheLiveSnapshot(t *testing.T) {
	dir := t.TempDir()
	s, err := Start(dir, "example-build", false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	at := time.Now().UTC().Add(-time.Minute)
	s.SetConfigUnavailable(&at)
	got, err := Query(context.Background(), dir)
	if err != nil || got.ConfigUnavailableSince == nil || !got.ConfigUnavailableSince.Equal(at) {
		t.Fatalf("%+v %v", got, err)
	}
	s.SetConfigUnavailable(nil)
	got, err = Query(context.Background(), dir)
	if err != nil || got.ConfigUnavailableSince != nil {
		t.Fatalf("%+v %v", got, err)
	}
}
