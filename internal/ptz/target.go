package ptz

import (
	"errors"
	"net"
	"net/url"

	"pocketnvr/internal/config"
)

// Target is where and how to reach a camera's ONVIF device service.
type Target struct {
	// Endpoints are candidate device_service URLs, tried in order. They never
	// carry credentials.
	Endpoints []string
	User      string
	Pass      string
}

// Common ONVIF device-service ports probed when onvif_url is not configured.
var defaultONVIFPorts = []string{"2020", "80"}

// TargetFor derives the ONVIF target for cam. Credentials come from
// source_main (percent-decoded), overridden by any userinfo in onvif_url.
// Errors never include the offending URL, since it may carry credentials.
func TargetFor(cam config.Camera) (Target, error) {
	var t Target
	var src *url.URL
	if cam.SourceMain != "" {
		u, err := url.Parse(cam.SourceMain)
		if err != nil {
			return Target{}, errors.New("ptz: source_main is not a valid URL")
		}
		src = u
		if u.User != nil {
			t.User = u.User.Username()
			t.Pass, _ = u.User.Password()
		}
	}

	if cam.OnvifURL != "" {
		u, err := url.Parse(cam.OnvifURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return Target{}, errors.New("ptz: onvif_url must be an http(s) URL with a host")
		}
		if u.User != nil {
			t.User = u.User.Username()
			t.Pass, _ = u.User.Password()
			u.User = nil
		}
		t.Endpoints = []string{u.String()}
		return t, nil
	}

	if src == nil || src.Hostname() == "" {
		return Target{}, errors.New("ptz: source_main has no host to derive an ONVIF endpoint from")
	}
	for _, port := range defaultONVIFPorts {
		t.Endpoints = append(t.Endpoints, "http://"+net.JoinHostPort(src.Hostname(), port)+"/onvif/device_service")
	}
	return t, nil
}
