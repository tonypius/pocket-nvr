package ptz

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	nsDevice = "http://www.onvif.org/ver10/device/wsdl"
	nsMedia  = "http://www.onvif.org/ver10/media/wsdl"
	nsPTZ    = "http://www.onvif.org/ver20/ptz/wsdl"
	nsSchema = "http://www.onvif.org/ver10/schema"

	moveSpeed = 0.5
)

// Caps is what a camera's PTZ node can do.
type Caps struct {
	PanTilt bool `json:"pan_tilt"`
	Zoom    bool `json:"zoom"`
}

// Direction is a PTZ movement request.
type Direction string

const (
	Up      Direction = "up"
	Down    Direction = "down"
	Left    Direction = "left"
	Right   Direction = "right"
	ZoomIn  Direction = "zoom_in"
	ZoomOut Direction = "zoom_out"
)

// ParseDirection validates s as a Direction.
func ParseDirection(s string) (Direction, bool) {
	switch d := Direction(s); d {
	case Up, Down, Left, Right, ZoomIn, ZoomOut:
		return d, true
	}
	return "", false
}

// velocity returns the pan, tilt and zoom components for d.
func (d Direction) velocity() (pan, tilt, zoom float64, ok bool) {
	switch d {
	case Left:
		return -moveSpeed, 0, 0, true
	case Right:
		return moveSpeed, 0, 0, true
	case Down:
		return 0, -moveSpeed, 0, true
	case Up:
		return 0, moveSpeed, 0, true
	case ZoomOut:
		return 0, 0, -moveSpeed, true
	case ZoomIn:
		return 0, 0, moveSpeed, true
	}
	return 0, 0, 0, false
}

// Client controls one camera's PTZ over ONVIF. It is safe for concurrent
// use: everything but the SOAP client's internal state is fixed by Dial.
type Client struct {
	sc      *soapClient
	ptzURL  string // empty when the camera has no usable PTZ profile
	profile string // profile token carrying a PTZ configuration
	caps    Caps
}

type capabilitiesResp struct {
	MediaXAddr string `xml:"Capabilities>Media>XAddr"`
	PTZXAddr   string `xml:"Capabilities>PTZ>XAddr"`
}

type profilesResp struct {
	Profiles []struct {
		Token string `xml:"token,attr"`
		PTZ   *struct {
			NodeToken string `xml:"NodeToken"`
		} `xml:"PTZConfiguration"`
	} `xml:"Profiles"`
}

type nodesResp struct {
	Nodes []struct {
		Token   string    `xml:"token,attr"`
		PanTilt *struct{} `xml:"SupportedPTZSpaces>ContinuousPanTiltVelocitySpace"`
		Zoom    *struct{} `xml:"SupportedPTZSpaces>ContinuousZoomVelocitySpace"`
	} `xml:"PTZNode"`
}

// Dial connects to the first of t.Endpoints that answers GetCapabilities and
// discovers the camera's PTZ profile and abilities.
//
// It returns an error when no endpoint answered (unreachable, bad
// credentials, ctx expiry, ...). A camera that answered but has no PTZ
// service or no profile with a PTZ configuration yields a usable Client with
// empty Caps and a nil error.
func Dial(ctx context.Context, t Target, hc *http.Client) (*Client, error) {
	if len(t.Endpoints) == 0 {
		return nil, errors.New("ptz: no ONVIF endpoints to try")
	}
	var errs []error
	for i, ep := range t.Endpoints {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		sc := newSoap(ep, t.User, t.Pass, hc)
		var caps capabilitiesResp
		err := probeEndpoint(ctx, sc, ep, len(t.Endpoints)-i, &caps)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", ep, err))
			continue
		}
		return finishDial(ctx, sc, ep, caps)
	}
	return nil, fmt.Errorf("ptz: no ONVIF endpoint answered: %w", errors.Join(errs...))
}

// probeEndpoint sends GetCapabilities to ep. Each endpoint gets its own
// sub-deadline, an equal share of the time ctx has left among the remaining
// endpoints (left counts ep itself), so a filtered port that swallows the
// request cannot starve the endpoints still to be tried. Without a deadline on
// ctx none is added.
func probeEndpoint(ctx context.Context, sc *soapClient, ep string, left int, caps *capabilitiesResp) error {
	if dl, ok := ctx.Deadline(); ok && left > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Until(dl)/time.Duration(left))
		defer cancel()
	}
	return sc.call(ctx, ep, `<tds:GetCapabilities xmlns:tds="`+nsDevice+`"><tds:Category>All</tds:Category></tds:GetCapabilities>`, caps)
}

// finishDial runs after an endpoint answered: it resolves the media and PTZ
// services, picks the PTZ profile and reads the node's abilities.
func finishDial(ctx context.Context, sc *soapClient, ep string, caps capabilitiesResp) (*Client, error) {
	c := &Client{sc: sc}
	ptzURL := rewriteXAddr(caps.PTZXAddr, ep)
	mediaURL := rewriteXAddr(caps.MediaXAddr, ep)
	if ptzURL == "" || mediaURL == "" {
		return c, nil // answered, but no PTZ (or nothing to find profiles with)
	}

	var profs profilesResp
	if err := sc.call(ctx, mediaURL, `<trt:GetProfiles xmlns:trt="`+nsMedia+`"/>`, &profs); err != nil {
		return nil, fmt.Errorf("ptz: GetProfiles: %w", err)
	}
	var nodeToken string
	for _, p := range profs.Profiles {
		if p.PTZ != nil && p.Token != "" {
			c.profile, nodeToken = p.Token, p.PTZ.NodeToken
			break
		}
	}
	if c.profile == "" {
		return c, nil
	}

	var nodes nodesResp
	if err := sc.call(ctx, ptzURL, `<tptz:GetNodes xmlns:tptz="`+nsPTZ+`"/>`, &nodes); err != nil {
		return nil, fmt.Errorf("ptz: GetNodes: %w", err)
	}
	// Prefer the node the profile is bound to; otherwise fall back to the first.
	for i, n := range nodes.Nodes {
		if n.Token == nodeToken || i == 0 {
			c.caps = Caps{PanTilt: n.PanTilt != nil, Zoom: n.Zoom != nil}
		}
		if n.Token == nodeToken {
			break
		}
	}
	if c.caps == (Caps{}) {
		c.profile = "" // nothing controllable: behave like a camera without PTZ
		return c, nil
	}
	c.ptzURL = ptzURL
	return c, nil
}

// rewriteXAddr replaces the scheme and host:port of an advertised service
// address with those of the endpoint that answered, since cameras commonly
// advertise internal or otherwise unreachable addresses. It returns "" for an
// empty or unusable address.
func rewriteXAddr(xaddr, endpoint string) string {
	// encoding/xml keeps whitespace in character data, so a pretty-printed
	// <XAddr> arrives padded with newlines and indentation.
	xaddr = strings.TrimSpace(xaddr)
	if xaddr == "" {
		return ""
	}
	x, err := url.Parse(xaddr)
	if err != nil {
		return ""
	}
	e, err := url.Parse(endpoint)
	if err != nil {
		return ""
	}
	x.Scheme, x.Host, x.User = e.Scheme, e.Host, nil
	return x.String()
}

// Capabilities reports what the camera's PTZ node supports, as determined
// during Dial. It is empty for a camera without usable PTZ.
func (c *Client) Capabilities() Caps { return c.caps }

var errNoPTZ = errors.New("ptz: camera has no PTZ profile")

func escape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// durationXSD formats d as an xsd:duration of seconds, e.g. PT3S, PT1.5S.
func durationXSD(d time.Duration) string {
	return "PT" + strconv.FormatFloat(d.Seconds(), 'f', -1, 64) + "S"
}

func fmtVel(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// Move starts a continuous move in direction d at fixed speed. The camera
// stops by itself after timeout (omitted from the request when not positive,
// in which case only Stop ends the move).
func (c *Client) Move(ctx context.Context, d Direction, timeout time.Duration) error {
	if c.ptzURL == "" {
		return errNoPTZ
	}
	pan, tilt, zoom, ok := d.velocity()
	if !ok {
		return fmt.Errorf("ptz: unknown direction %q", string(d))
	}
	body := `<tptz:ContinuousMove xmlns:tptz="` + nsPTZ + `" xmlns:tt="` + nsSchema + `">` +
		`<tptz:ProfileToken>` + escape(c.profile) + `</tptz:ProfileToken>` +
		`<tptz:Velocity>`
	// Only the moved axis is sent: ONVIF has unsupported spaces omitted and
	// some firmware faults on a Zoom velocity for a node without zoom.
	if d == ZoomIn || d == ZoomOut {
		body += `<tt:Zoom x="` + fmtVel(zoom) + `"/>`
	} else {
		body += `<tt:PanTilt x="` + fmtVel(pan) + `" y="` + fmtVel(tilt) + `"/>`
	}
	body += `</tptz:Velocity>`
	if timeout > 0 {
		body += `<tptz:Timeout>` + durationXSD(timeout) + `</tptz:Timeout>`
	}
	body += `</tptz:ContinuousMove>`
	return c.sc.call(ctx, c.ptzURL, body, nil)
}

// Stop halts any pan/tilt and zoom movement.
func (c *Client) Stop(ctx context.Context) error {
	if c.ptzURL == "" {
		return errNoPTZ
	}
	body := `<tptz:Stop xmlns:tptz="` + nsPTZ + `">` +
		`<tptz:ProfileToken>` + escape(c.profile) + `</tptz:ProfileToken>` +
		`<tptz:PanTilt>true</tptz:PanTilt><tptz:Zoom>true</tptz:Zoom></tptz:Stop>`
	return c.sc.call(ctx, c.ptzURL, body, nil)
}
