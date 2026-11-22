// Package ptz implements ONVIF PTZ control over SOAP with WS-Security.
package ptz

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // WS-UsernameToken PasswordDigest mandates SHA-1.
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	nsWSSE = "http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd"
	nsWSU  = "http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-utility-1.0.xsd"

	passwordDigestType = "http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordDigest"
	base64BinaryType   = "http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-soap-message-security-1.0#Base64Binary"

	wsseTimeLayout  = "2006-01-02T15:04:05Z"
	maxResponseSize = 4 << 20

	defaultHTTPTimeout = 10 * time.Second
	clockSyncTimeout   = 5 * time.Second
)

// ErrUnauthorized is returned (wrapped) when the device answers a request with
// a plain HTTP 401 instead of a SOAP Fault. The message carries no URL or
// credentials.
var ErrUnauthorized = errors.New("soap: HTTP 401 unauthorized")

// Fault is a SOAP Fault returned by the device.
type Fault struct{ Code, Reason string }

func (f *Fault) Error() string {
	switch {
	case f.Code != "" && f.Reason != "":
		return "soap fault " + f.Code + ": " + f.Reason
	case f.Code != "":
		return "soap fault " + f.Code
	default:
		return "soap fault: " + f.Reason
	}
}

// wsseHeader returns the <wsse:Security> fragment carrying a UsernameToken
// with PasswordDigest = base64(sha1(nonce + created + password)).
func wsseHeader(user, pass string, nonce []byte, created time.Time) string {
	ts := created.UTC().Format(wsseTimeLayout)
	h := sha1.New() //nolint:gosec
	h.Write(nonce)
	h.Write([]byte(ts))
	h.Write([]byte(pass))
	digest := base64.StdEncoding.EncodeToString(h.Sum(nil))

	var u bytes.Buffer
	_ = xml.EscapeText(&u, []byte(user))
	return `<wsse:Security xmlns:wsse="` + nsWSSE + `" xmlns:wsu="` + nsWSU + `">` +
		`<wsse:UsernameToken>` +
		`<wsse:Username>` + u.String() + `</wsse:Username>` +
		`<wsse:Password Type="` + passwordDigestType + `">` + digest + `</wsse:Password>` +
		`<wsse:Nonce EncodingType="` + base64BinaryType + `">` + base64.StdEncoding.EncodeToString(nonce) + `</wsse:Nonce>` +
		`<wsu:Created>` + ts + `</wsu:Created>` +
		`</wsse:UsernameToken></wsse:Security>`
}

// noRedirect is the CheckRedirect for every ONVIF http.Client: a camera must
// not be able to bounce the SOAP POST (which carries the WSSE digest) to
// another host. The 3xx response is returned as is and reported as an error.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// soapClient is a minimal SOAP 1.2 client with WS-Security UsernameToken auth.
type soapClient struct {
	deviceURL string
	user      string
	pass      string
	hc        *http.Client

	mu     sync.Mutex    // guards offset
	offset time.Duration // device UTC minus local clock

	syncMu sync.Mutex
	syncCh chan struct{} // nil until the lazy sync starts; closed when it finishes
}

func newSoap(deviceURL, user, pass string, hc *http.Client) *soapClient {
	if hc == nil {
		// Never fall back to http.DefaultClient: it has no timeout, so a
		// camera that accepts a connection but never replies would hang us.
		hc = &http.Client{Timeout: defaultHTTPTimeout, CheckRedirect: noRedirect}
	}
	return &soapClient{deviceURL: deviceURL, user: user, pass: pass, hc: hc}
}

const systemDateTimeBody = `<tds:GetSystemDateAndTime xmlns:tds="http://www.onvif.org/ver10/device/wsdl"/>`

type systemDateTimeResp struct {
	Year   int `xml:"SystemDateAndTime>UTCDateTime>Date>Year"`
	Month  int `xml:"SystemDateAndTime>UTCDateTime>Date>Month"`
	Day    int `xml:"SystemDateAndTime>UTCDateTime>Date>Day"`
	Hour   int `xml:"SystemDateAndTime>UTCDateTime>Time>Hour"`
	Minute int `xml:"SystemDateAndTime>UTCDateTime>Time>Minute"`
	Second int `xml:"SystemDateAndTime>UTCDateTime>Time>Second"`
}

// syncClock queries the device's UTC time (unauthenticated) and stores
// offset = deviceUTC - local now.
func (s *soapClient) syncClock(ctx context.Context) error {
	before := time.Now()
	var r systemDateTimeResp
	if err := s.do(ctx, s.deviceURL, systemDateTimeBody, &r, false); err != nil {
		return err
	}
	if r.Year == 0 || r.Month == 0 || r.Day == 0 {
		return errors.New("soap: device returned no UTC date/time")
	}
	after := time.Now()
	dev := time.Date(r.Year, time.Month(r.Month), r.Day, r.Hour, r.Minute, r.Second, 0, time.UTC)
	mid := before.Add(after.Sub(before) / 2) // compensate for half the round trip
	s.mu.Lock()
	s.offset = dev.Sub(mid)
	s.mu.Unlock()
	return nil
}

// ensureSynced makes sure the one-time clock sync has finished, starting it
// if this is the first caller. The sync runs in its own goroutine under a
// bounded context detached from any caller's cancellation, so a first caller
// with a short deadline cannot poison it; every caller waits on its own ctx.
// A failed sync is not fatal (offset stays 0) and is not retried.
func (s *soapClient) ensureSynced(ctx context.Context) error {
	s.syncMu.Lock()
	if s.syncCh == nil {
		ch := make(chan struct{})
		s.syncCh = ch
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), clockSyncTimeout)
		go func() {
			defer close(ch)
			defer cancel()
			_ = s.syncClock(sctx) // best effort: devices that lack it are still usable
		}()
	}
	ch := s.syncCh
	s.syncMu.Unlock()

	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("soap: waiting for clock sync: %w", ctx.Err())
	}
}

// call POSTs bodyXML (the full child element(s) of <Body>) to url with a
// WSSE header and unmarshals the first element inside the response <Body>
// (the response element) into out (may be nil); field paths in out are
// relative to that element. Matching is by local name, so namespace
// prefixes do not matter. SOAP Faults are returned as *Fault.
func (s *soapClient) call(ctx context.Context, url, bodyXML string, out any) error {
	if s.user != "" {
		if err := s.ensureSynced(ctx); err != nil {
			return err
		}
	}
	return s.do(ctx, url, bodyXML, out, s.user != "")
}

type envelope struct {
	Body struct {
		Fault *struct {
			// SOAP 1.2
			Code    string `xml:"Code>Value"`
			Subcode string `xml:"Code>Subcode>Value"`
			Reason  string `xml:"Reason>Text"`
			// SOAP 1.1
			FaultCode   string `xml:"faultcode"`
			FaultString string `xml:"faultstring"`
		} `xml:"Fault"`
		Inner []byte `xml:",innerxml"`
	} `xml:"Body"`
}

func (s *soapClient) do(ctx context.Context, url, bodyXML string, out any, auth bool) error {
	var hdr string
	if auth {
		nonce := make([]byte, 16)
		if _, err := rand.Read(nonce); err != nil {
			return fmt.Errorf("soap: nonce: %w", err)
		}
		s.mu.Lock()
		off := s.offset
		s.mu.Unlock()
		hdr = "<s:Header>" + wsseHeader(s.user, s.pass, nonce, time.Now().Add(off)) + "</s:Header>"
	}
	payload := `<?xml version="1.0" encoding="utf-8"?>` +
		`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">` +
		hdr + `<s:Body>` + bodyXML + `</s:Body></s:Envelope>`

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(payload))
	if err != nil {
		return fmt.Errorf("soap: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/soap+xml; charset=utf-8")
	resp, err := s.hc.Do(req)
	if err != nil {
		return fmt.Errorf("soap: request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return fmt.Errorf("soap: read response: %w", err)
	}

	var env envelope
	parseErr := xml.Unmarshal(raw, &env)
	if parseErr == nil && env.Body.Fault != nil {
		f := env.Body.Fault
		fault := &Fault{Code: f.Subcode, Reason: strings.TrimSpace(f.Reason)}
		if fault.Code == "" {
			fault.Code = f.Code
		}
		if fault.Code == "" {
			fault.Code = f.FaultCode
		}
		if fault.Reason == "" {
			fault.Reason = strings.TrimSpace(f.FaultString)
		}
		return fault
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("soap: unexpected HTTP status %d", resp.StatusCode)
	}
	if parseErr != nil {
		return fmt.Errorf("soap: parse response: %w", parseErr)
	}
	if out == nil {
		return nil
	}
	if err := xml.Unmarshal(env.Body.Inner, out); err != nil {
		return fmt.Errorf("soap: decode body: %w", err)
	}
	return nil
}
