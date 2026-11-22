// Package ptztest provides a fake ONVIF camera for tests. It must not import
// pocketnvr/internal/ptz (in-package tests there would create an import cycle).
package ptztest

import (
	"crypto/sha1" //nolint:gosec // WS-UsernameToken digest.
	"crypto/subtle"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Options configures a fake camera.
type Options struct {
	User, Pass string
	// ClockSkew is added to the real clock to produce the camera's clock.
	ClockSkew time.Duration
	// Delay holds the response to the named operations (SOAP local name,
	// e.g. "GetSystemDateAndTime") for the given duration, or until the
	// client disconnects or the camera is closed.
	Delay map[string]time.Duration

	// PanTilt and Zoom make the PTZ node advertise continuous pan/tilt and
	// continuous zoom velocity spaces respectively.
	PanTilt, Zoom bool
	// NoPTZService omits the PTZ service from GetCapabilities and the PTZ
	// configuration from every profile.
	NoPTZService bool
	// AdvertiseHost is the host placed in advertised service XAddrs. It
	// defaults to the non-routable 192.0.2.1, like cameras that advertise an
	// internal address.
	AdvertiseHost string
	// PrettyXAddrs pads every advertised XAddr with newlines and indentation,
	// like cameras that pretty-print their XML.
	PrettyXAddrs bool
}

// Move is one recorded ContinuousMove request.
type Move struct {
	Pan, Tilt, Zoom float64
	// HasPanTilt and HasZoom report whether the request carried a
	// <PanTilt> / <Zoom> velocity element at all.
	HasPanTilt, HasZoom bool
	Timeout             string // xsd:duration as sent, e.g. "PT3S"
}

const (
	// ptzProfileToken is the only profile carrying a PTZ configuration; the
	// first profile deliberately has none.
	ptzProfileToken   = "profile_ptz"
	videoProfileToken = "profile_video"
)

// Camera is a running fake ONVIF camera.
type Camera struct {
	// URL is the device-service endpoint (<server>/onvif/device_service).
	URL string

	opts Options
	srv  *httptest.Server
	done chan struct{} // closed on shutdown to release delayed handlers
	reqs atomic.Int64
	ops  map[string]operation

	mu       sync.Mutex // guards the fields below
	moves    []Move
	stops    int
	inflight int // PTZ requests currently being served
	maxConc  int
}

// request is what an operation handler sees.
type request struct {
	// Name is the local name of the first element inside the SOAP Body.
	Name string
	// Inner is the raw XML content of that element.
	Inner []byte
}

// operation produces the XML for the response Body (a full element), or a
// *fault. Handlers are registered by SOAP operation local name; add new
// operations in registerOps.
type operation struct {
	auth   bool
	ptz    bool // counted by MaxConcurrent
	handle func(c *Camera, r request) (string, *fault)
}

type fault struct{ subcode, reason string }

// New starts a fake camera that is shut down with t.
func New(t testing.TB, o Options) *Camera {
	t.Helper()
	c := &Camera{opts: o, ops: map[string]operation{}, done: make(chan struct{})}
	c.registerOps()
	c.srv = httptest.NewServer(http.HandlerFunc(c.serve))
	t.Cleanup(func() {
		close(c.done)
		c.srv.Close()
	})
	c.URL = c.srv.URL + "/onvif/device_service"
	return c
}

// Requests returns the number of SOAP requests served so far.
func (c *Camera) Requests() int { return int(c.reqs.Load()) }

// Moves returns the ContinuousMove requests received so far, in order.
func (c *Camera) Moves() []Move {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Move(nil), c.moves...)
}

// Stops returns the number of Stop requests received so far.
func (c *Camera) Stops() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stops
}

// MaxConcurrent returns the peak number of PTZ requests (ContinuousMove,
// Stop) that were in flight at the same time.
func (c *Camera) MaxConcurrent() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.maxConc
}

// now is the camera's clock.
func (c *Camera) now() time.Time { return time.Now().Add(c.opts.ClockSkew) }

func (c *Camera) registerOps() {
	c.ops["GetSystemDateAndTime"] = operation{handle: func(c *Camera, _ request) (string, *fault) {
		t := c.now().UTC()
		return fmt.Sprintf(`<tds:GetSystemDateAndTimeResponse xmlns:tds="http://www.onvif.org/ver10/device/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema">`+
			`<tds:SystemDateAndTime><tt:DateTimeType>NTP</tt:DateTimeType><tt:DaylightSavings>false</tt:DaylightSavings>`+
			`<tt:UTCDateTime><tt:Time><tt:Hour>%d</tt:Hour><tt:Minute>%d</tt:Minute><tt:Second>%d</tt:Second></tt:Time>`+
			`<tt:Date><tt:Year>%d</tt:Year><tt:Month>%d</tt:Month><tt:Day>%d</tt:Day></tt:Date></tt:UTCDateTime>`+
			`</tds:SystemDateAndTime></tds:GetSystemDateAndTimeResponse>`,
			t.Hour(), t.Minute(), t.Second(), t.Year(), int(t.Month()), t.Day()), nil
	}}
	c.registerPTZOps()
	c.ops["GetDeviceInformation"] = operation{auth: true, handle: func(c *Camera, _ request) (string, *fault) {
		return `<tds:GetDeviceInformationResponse xmlns:tds="http://www.onvif.org/ver10/device/wsdl">` +
			`<tds:Manufacturer>FakeCam</tds:Manufacturer><tds:Model>F1</tds:Model>` +
			`<tds:FirmwareVersion>1.0</tds:FirmwareVersion><tds:SerialNumber>0001</tds:SerialNumber>` +
			`<tds:HardwareId>fake</tds:HardwareId></tds:GetDeviceInformationResponse>`, nil
	}}
}

func (c *Camera) advertiseHost() string {
	if c.opts.AdvertiseHost != "" {
		return c.opts.AdvertiseHost
	}
	return "192.0.2.1"
}

// xaddr renders an advertised service address element.
func (c *Camera) xaddr(path string) string {
	u := "http://" + c.advertiseHost() + path
	if c.opts.PrettyXAddrs {
		u = "\n        " + u + "\n      "
	}
	return `<tt:XAddr>` + u + `</tt:XAddr>`
}

func (c *Camera) registerPTZOps() {
	c.ops["GetCapabilities"] = operation{auth: true, handle: func(c *Camera, _ request) (string, *fault) {
		ptz := ""
		if !c.opts.NoPTZService {
			ptz = `<tt:PTZ>` + c.xaddr("/onvif/ptz_service") + `</tt:PTZ>`
		}
		return `<tds:GetCapabilitiesResponse xmlns:tds="http://www.onvif.org/ver10/device/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema">` +
			`<tds:Capabilities><tt:Device>` + c.xaddr("/onvif/device_service") + `</tt:Device>` +
			`<tt:Media>` + c.xaddr("/onvif/media_service") + `</tt:Media>` + ptz +
			`</tds:Capabilities></tds:GetCapabilitiesResponse>`, nil
	}}
	c.ops["GetProfiles"] = operation{auth: true, handle: func(c *Camera, _ request) (string, *fault) {
		cfg := ""
		if !c.opts.NoPTZService {
			cfg = `<tt:PTZConfiguration token="ptzcfg"><tt:Name>PTZ</tt:Name><tt:NodeToken>node1</tt:NodeToken></tt:PTZConfiguration>`
		}
		return `<trt:GetProfilesResponse xmlns:trt="http://www.onvif.org/ver10/media/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema">` +
			`<trt:Profiles token="` + videoProfileToken + `" fixed="true"><tt:Name>video</tt:Name></trt:Profiles>` +
			`<trt:Profiles token="` + ptzProfileToken + `" fixed="true"><tt:Name>ptz</tt:Name>` + cfg + `</trt:Profiles>` +
			`</trt:GetProfilesResponse>`, nil
	}}
	c.ops["GetNodes"] = operation{auth: true, handle: func(c *Camera, _ request) (string, *fault) {
		spaces := `<tt:AbsolutePanTiltPositionSpace><tt:URI>http://www.onvif.org/ver10/tptz/PanTiltSpaces/PositionGenericSpace</tt:URI></tt:AbsolutePanTiltPositionSpace>`
		if c.opts.PanTilt {
			spaces += `<tt:ContinuousPanTiltVelocitySpace><tt:URI>http://www.onvif.org/ver10/tptz/PanTiltSpaces/VelocityGenericSpace</tt:URI></tt:ContinuousPanTiltVelocitySpace>`
		}
		if c.opts.Zoom {
			spaces += `<tt:ContinuousZoomVelocitySpace><tt:URI>http://www.onvif.org/ver10/tptz/ZoomSpaces/VelocityGenericSpace</tt:URI></tt:ContinuousZoomVelocitySpace>`
		}
		return `<tptz:GetNodesResponse xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema">` +
			`<tptz:PTZNode token="node1"><tt:Name>node</tt:Name><tt:SupportedPTZSpaces>` + spaces + `</tt:SupportedPTZSpaces>` +
			`</tptz:PTZNode></tptz:GetNodesResponse>`, nil
	}}
	c.ops["ContinuousMove"] = operation{auth: true, ptz: true, handle: func(c *Camera, r request) (string, *fault) {
		var req struct {
			Profile  string `xml:"ProfileToken"`
			Velocity struct {
				PanTilt *struct {
					X float64 `xml:"x,attr"`
					Y float64 `xml:"y,attr"`
				} `xml:"PanTilt"`
				Zoom *struct {
					X float64 `xml:"x,attr"`
				} `xml:"Zoom"`
			} `xml:"Velocity"`
			Timeout string `xml:"Timeout"`
		}
		// The inner XML has several top-level elements; wrap it so it parses.
		if err := xml.Unmarshal([]byte("<r>"+string(r.Inner)+"</r>"), &req); err != nil {
			return "", &fault{"ter:InvalidArgVal", "malformed ContinuousMove"}
		}
		if req.Profile != ptzProfileToken {
			return "", &fault{"ter:NoProfile", "profile does not exist or has no PTZ configuration"}
		}
		mv := Move{Timeout: req.Timeout}
		if pt := req.Velocity.PanTilt; pt != nil {
			mv.HasPanTilt, mv.Pan, mv.Tilt = true, pt.X, pt.Y
		}
		if z := req.Velocity.Zoom; z != nil {
			mv.HasZoom, mv.Zoom = true, z.X
		}
		c.mu.Lock()
		c.moves = append(c.moves, mv)
		c.mu.Unlock()
		return `<tptz:ContinuousMoveResponse xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl"/>`, nil
	}}
	c.ops["Stop"] = operation{auth: true, ptz: true, handle: func(c *Camera, r request) (string, *fault) {
		var req struct {
			Profile string `xml:"ProfileToken"`
		}
		if err := xml.Unmarshal([]byte("<r>"+string(r.Inner)+"</r>"), &req); err != nil {
			return "", &fault{"ter:InvalidArgVal", "malformed Stop"}
		}
		if req.Profile != ptzProfileToken {
			return "", &fault{"ter:NoProfile", "profile does not exist or has no PTZ configuration"}
		}
		c.mu.Lock()
		c.stops++
		c.mu.Unlock()
		return `<tptz:StopResponse xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl"/>`, nil
	}}
}

type envelope struct {
	Security struct {
		Username string `xml:"UsernameToken>Username"`
		Password string `xml:"UsernameToken>Password"`
		Nonce    string `xml:"UsernameToken>Nonce"`
		Created  string `xml:"UsernameToken>Created"`
	} `xml:"Header>Security"`
	Body struct {
		Op struct {
			XMLName xml.Name
			Inner   []byte `xml:",innerxml"`
		} `xml:",any"`
	} `xml:"Body"`
}

func (c *Camera) serve(w http.ResponseWriter, r *http.Request) {
	c.reqs.Add(1)
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var env envelope
	if err := xml.Unmarshal(raw, &env); err != nil || env.Body.Op.XMLName.Local == "" {
		writeFault(w, &fault{"ter:InvalidArgs", "malformed SOAP request"})
		return
	}
	op, ok := c.ops[env.Body.Op.XMLName.Local]
	if ok && op.ptz {
		c.mu.Lock()
		c.inflight++
		if c.inflight > c.maxConc {
			c.maxConc = c.inflight
		}
		c.mu.Unlock()
		defer func() {
			c.mu.Lock()
			c.inflight--
			c.mu.Unlock()
		}()
	}
	if d := c.opts.Delay[env.Body.Op.XMLName.Local]; d > 0 {
		select {
		case <-time.After(d):
		case <-r.Context().Done():
			return
		case <-c.done:
			return
		}
	}
	if !ok {
		writeFault(w, &fault{"ter:ActionNotSupported", "unsupported operation " + env.Body.Op.XMLName.Local})
		return
	}
	if op.auth {
		if f := c.authenticate(env); f != nil {
			writeFault(w, f)
			return
		}
	}
	body, f := op.handle(c, request{Name: env.Body.Op.XMLName.Local, Inner: env.Body.Op.Inner})
	if f != nil {
		writeFault(w, f)
		return
	}
	w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
	fmt.Fprint(w, `<?xml version="1.0" encoding="utf-8"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body>`+body+`</s:Body></s:Envelope>`)
}

// authenticate verifies the WSSE UsernameToken digest and freshness.
func (c *Camera) authenticate(env envelope) *fault {
	deny := &fault{"ter:NotAuthorized", "Sender not authorized"}
	sec := env.Security
	if sec.Username != c.opts.User || sec.Password == "" {
		return deny
	}
	nonce, err := base64.StdEncoding.DecodeString(sec.Nonce)
	if err != nil {
		return deny
	}
	created, err := time.Parse("2006-01-02T15:04:05Z", sec.Created)
	if err != nil {
		return deny
	}
	if d := created.Sub(c.now()); d < -5*time.Second || d > 5*time.Second {
		return deny
	}
	h := sha1.New() //nolint:gosec
	h.Write(nonce)
	h.Write([]byte(sec.Created))
	h.Write([]byte(c.opts.Pass))
	want := base64.StdEncoding.EncodeToString(h.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(want), []byte(sec.Password)) != 1 {
		return deny
	}
	return nil
}

func writeFault(w http.ResponseWriter, f *fault) {
	w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
	w.WriteHeader(http.StatusInternalServerError)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="utf-8"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:ter="http://www.onvif.org/ver10/error"><s:Body><s:Fault>`+
		`<s:Code><s:Value>s:Sender</s:Value><s:Subcode><s:Value>%s</s:Value></s:Subcode></s:Code>`+
		`<s:Reason><s:Text xml:lang="en">%s</s:Text></s:Reason></s:Fault></s:Body></s:Envelope>`, f.subcode, f.reason)
}
