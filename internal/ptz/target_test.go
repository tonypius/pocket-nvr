package ptz

import (
	"reflect"
	"strings"
	"testing"

	"pocketnvr/internal/config"
)

func TestTargetForDerivesFromSourceMain(t *testing.T) {
	got, err := TargetFor(config.Camera{SourceMain: "rtsp://u:p@10.0.0.9:554/stream1"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"http://10.0.0.9:2020/onvif/device_service",
		"http://10.0.0.9:80/onvif/device_service",
	}
	if !reflect.DeepEqual(got.Endpoints, want) {
		t.Errorf("Endpoints = %v, want %v", got.Endpoints, want)
	}
	if got.User != "u" || got.Pass != "p" {
		t.Errorf("creds = %q/%q, want u/p", got.User, got.Pass)
	}
}

func TestTargetForDecodesSpecialCharPassword(t *testing.T) {
	got, err := TargetFor(config.Camera{SourceMain: "rtsp://camadmin:Sup3r%24ecret%40x%3A1@10.0.0.9/s"})
	if err != nil {
		t.Fatal(err)
	}
	if got.User != "camadmin" || got.Pass != "Sup3r$ecret@x:1" {
		t.Errorf("creds = %q/%q", got.User, got.Pass)
	}

	got, err = TargetFor(config.Camera{SourceMain: "rtsp://camadmin:Sup3r$ecret@10.0.0.9/s"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Pass != "Sup3r$ecret" {
		t.Errorf("raw $ password = %q, want Sup3r$ecret", got.Pass)
	}
}

func TestTargetForOnvifURLWins(t *testing.T) {
	got, err := TargetFor(config.Camera{
		SourceMain: "rtsp://u:p@10.0.0.9:554/stream1",
		OnvifURL:   "http://10.0.0.9:8899/onvif/device_service",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"http://10.0.0.9:8899/onvif/device_service"}
	if !reflect.DeepEqual(got.Endpoints, want) {
		t.Errorf("Endpoints = %v, want %v", got.Endpoints, want)
	}
	if got.User != "u" || got.Pass != "p" {
		t.Errorf("creds = %q/%q, want u/p", got.User, got.Pass)
	}
}

func TestTargetForOnvifURLEmbeddedCreds(t *testing.T) {
	got, err := TargetFor(config.Camera{
		SourceMain: "rtsp://u:p@10.0.0.9:554/stream1",
		OnvifURL:   "http://ou:op%40x@10.0.0.9:8899/onvif/device_service",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.User != "ou" || got.Pass != "op@x" {
		t.Errorf("creds = %q/%q, want ou/op@x", got.User, got.Pass)
	}
	want := []string{"http://10.0.0.9:8899/onvif/device_service"}
	if !reflect.DeepEqual(got.Endpoints, want) {
		t.Errorf("Endpoints = %v, want %v (creds must be stripped)", got.Endpoints, want)
	}
}

func TestTargetForIPv6Host(t *testing.T) {
	got, err := TargetFor(config.Camera{SourceMain: "rtsp://u:p@[fd00::9]:554/s"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Endpoints[0] != "http://[fd00::9]:2020/onvif/device_service" {
		t.Errorf("Endpoints[0] = %q", got.Endpoints[0])
	}
}

func TestTargetForErrorsDoNotLeakCredentials(t *testing.T) {
	for _, cam := range []config.Camera{
		{SourceMain: "rtsp://u:s3cret@:554/s"},         // no host
		{SourceMain: "rtsp://u:s3cret@bad host:554/s"}, // unparseable
		{SourceMain: ""}, // nothing to derive from
		{SourceMain: "rtsp://u:s3cret@h/s", OnvifURL: "::::"}, // bad onvif_url
		{SourceMain: "rtsp://u:s3cret@h/s", OnvifURL: "ftp://u:s3cret@h/x"},
	} {
		_, err := TargetFor(cam)
		if err == nil {
			t.Errorf("TargetFor(%+v): expected error", cam)
			continue
		}
		if strings.Contains(err.Error(), "s3cret") {
			t.Errorf("error leaks credentials: %v", err)
		}
	}
}
