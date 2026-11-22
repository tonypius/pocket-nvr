package ptz

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"pocketnvr/internal/ptz/ptztest"
)

const getDeviceInfoBody = `<tds:GetDeviceInformation xmlns:tds="http://www.onvif.org/ver10/device/wsdl"/>`

type deviceInfoResp struct {
	Manufacturer string `xml:"Manufacturer"`
}

func TestWSSEDigestVector(t *testing.T) {
	nonce, err := base64.StdEncoding.DecodeString("LKqI6G/AikKCQrN0zqZFlg==")
	if err != nil {
		t.Fatal(err)
	}
	created := time.Date(2010, 9, 16, 7, 50, 45, 0, time.UTC)
	h := wsseHeader("u", "userpassword", nonce, created)
	for _, want := range []string{
		"tuOSpGlFlIXsozq4HFNeeGeFLEI=",
		"LKqI6G/AikKCQrN0zqZFlg==",
		"2010-09-16T07:50:45Z",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("header missing %q:\n%s", want, h)
		}
	}
	if strings.Contains(h, "userpassword") {
		t.Errorf("header leaks plaintext password:\n%s", h)
	}
}

func TestSyncClockOffset(t *testing.T) {
	cam := ptztest.New(t, ptztest.Options{ClockSkew: time.Hour})
	s := newSoap(cam.URL, "u", "p", http.DefaultClient)
	if err := s.syncClock(context.Background()); err != nil {
		t.Fatalf("syncClock: %v", err)
	}
	if d := s.offset - time.Hour; d < -2*time.Second || d > 2*time.Second {
		t.Fatalf("offset = %v, want ~1h", s.offset)
	}
}

func TestCallSucceedsWithSkewedDeviceClock(t *testing.T) {
	cam := ptztest.New(t, ptztest.Options{User: "u", Pass: "p", ClockSkew: 2 * time.Hour})
	s := newSoap(cam.URL, "u", "p", http.DefaultClient)
	var out deviceInfoResp
	if err := s.call(context.Background(), cam.URL, getDeviceInfoBody, &out); err != nil {
		t.Fatalf("call: %v", err)
	}
	if out.Manufacturer == "" {
		t.Fatalf("response not unmarshaled: %+v", out)
	}
	// nil out is allowed.
	if err := s.call(context.Background(), cam.URL, getDeviceInfoBody, nil); err != nil {
		t.Fatalf("call with nil out: %v", err)
	}
}

func TestCallWrongPasswordIsFault(t *testing.T) {
	cam := ptztest.New(t, ptztest.Options{User: "u", Pass: "right"})
	s := newSoap(cam.URL, "u", "wrong-secret", http.DefaultClient)
	err := s.call(context.Background(), cam.URL, getDeviceInfoBody, nil)
	var f *Fault
	if !errors.As(err, &f) {
		t.Fatalf("want *Fault, got %T: %v", err, err)
	}
	if !strings.Contains(f.Code, "NotAuthorized") {
		t.Errorf("fault code = %q", f.Code)
	}
}

func TestCallDoesNotLeakPasswordInError(t *testing.T) {
	const pw = "s3cr3t-pw"
	cam := ptztest.New(t, ptztest.Options{User: "u", Pass: "other"})
	s := newSoap(cam.URL, "u", pw, http.DefaultClient)
	err := s.call(context.Background(), cam.URL, getDeviceInfoBody, nil)
	if err == nil || strings.Contains(err.Error(), pw) {
		t.Fatalf("bad fault error: %v", err)
	}

	// Transport failure (closed server) and non-SOAP HTTP error.
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()
	s2 := newSoap(url, "u", pw, http.DefaultClient)
	if err := s2.call(context.Background(), url, getDeviceInfoBody, nil); err == nil || strings.Contains(err.Error(), pw) {
		t.Fatalf("bad transport error: %v", err)
	}
	nf := httptest.NewServer(http.NotFoundHandler())
	defer nf.Close()
	s3 := newSoap(nf.URL, "u", pw, http.DefaultClient)
	if err := s3.call(context.Background(), nf.URL, getDeviceInfoBody, nil); err == nil || strings.Contains(err.Error(), pw) {
		t.Fatalf("bad http-status error: %v", err)
	}
}

func TestCallRespectsContext(t *testing.T) {
	cam := ptztest.New(t, ptztest.Options{User: "u", Pass: "p"})
	s := newSoap(cam.URL, "u", "p", http.DefaultClient)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.call(ctx, cam.URL, getDeviceInfoBody, nil); err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestClockSyncedOnceUnderConcurrency(t *testing.T) {
	cam := ptztest.New(t, ptztest.Options{User: "u", Pass: "p", ClockSkew: time.Hour})
	s := newSoap(cam.URL, "u", "p", http.DefaultClient)
	const n = 8
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- s.call(context.Background(), cam.URL, getDeviceInfoBody, nil)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("call: %v", err)
		}
	}
	// 1 sync + n authenticated calls.
	if got := cam.Requests(); got != n+1 {
		t.Errorf("requests = %d, want %d (exactly one clock sync)", got, n+1)
	}
}

func TestSyncFailureIsNotFatal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		if strings.Contains(string(buf[:n]), "GetSystemDateAndTime") {
			http.Error(w, "nope", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/soap+xml")
		fmt.Fprint(w, `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:x="urn:x"><s:Body><x:GetDeviceInformationResponse><x:Manufacturer>Acme</x:Manufacturer></x:GetDeviceInformationResponse></s:Body></s:Envelope>`)
	}))
	defer srv.Close()
	s := newSoap(srv.URL, "u", "p", http.DefaultClient)
	var out deviceInfoResp
	if err := s.call(context.Background(), srv.URL, getDeviceInfoBody, &out); err != nil {
		t.Fatalf("call: %v", err)
	}
	if out.Manufacturer != "Acme" || s.offset != 0 {
		t.Fatalf("out=%+v offset=%v", out, s.offset)
	}
}
