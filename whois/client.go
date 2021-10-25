package whois

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	wd "github.com/shlin168/go-whois/whois/domain"
	wip "github.com/shlin168/go-whois/whois/ip"
	"github.com/shlin168/go-whois/whois/utils"

	"github.com/sirupsen/logrus"
)

const (
	// Values of RespType
	RespTypeFound    = "found"
	RespTypeNotFound = "not_found"
	RespTypeError    = "error"
	RespTypeTimeout  = "timeout"

	// Values of AccType
	TypeDomain = "domain"
	TypeIP     = "ip"

	ianaWhoisServer = "whois.iana.org"
	whoisPort       = 43

	afrinicWhoisServer = "whois.afrinic.net"
	apnicWhoisServer   = "whois.apnic.net"
	arinWhoisServer    = "whois.arin.net"
	lacnicWhoisServer  = "whois.lacnic.net"
	ripeWhoisServer    = "whois.ripe.net"
)

var (
	readTimeout  = 1 * time.Second
	writeTimeout = 1 * time.Second

	// ErrDomainIPNotFound is fixed error message for WHOIS not found
	ErrDomainIPNotFound = errors.New("domain/ip not found")
	// ErrTimeout is fixed error message for timeout quering WHOIS server
	ErrTimeout = errors.New("timeout")
)

// FmtWhoisServer concate host and port to query whois
func FmtWhoisServer(host string, port int) string {
	return host + ":" + strconv.Itoa(port)
}

// Raw records rawtext from whois server
type Raw struct {
	Rawtext string
	Server  string // whois server that response the raw text
	Avail   *bool
}

// AsyncStatus records response status for async query
type AsyncStatus struct {
	DomainOrIP    string
	PublicSuffixs []string
	WhoisServer   string
	RespType      string
	Err           error
}

func NewAsyncStatus(ws string) *AsyncStatus {
	return &AsyncStatus{WhoisServer: ws}
}

func NewRaw(rawtext, server string, availPtn ...*regexp.Regexp) *Raw {
	nr := &Raw{Rawtext: rawtext, Server: server}
	if len(availPtn) > 0 {
		isavail := availPtn[0].Match([]byte(rawtext))
		nr.Avail = &isavail
	}
	return nr
}

// Client is used to query whois server to get latest whois information
type Client struct {
	dialer       *net.Dialer
	ianaServAddr string
	arinServAddr string
	arinMap      map[string]string
	whoisMap     DomainWhoisServerMap
	whoisPort    int
	timeout      time.Duration
	wtimeout     time.Duration
	rtimeout     time.Duration
	logger       logrus.FieldLogger
}

// NewClient initializes whois client for library used
func NewClient(timeout time.Duration) (*Client, error) {
	serverMap, err := NewDomainWhoisServerMap(WhoisServerListURL)
	if err != nil {
		return nil, err
	}
	return NewClientWithServerMap(timeout, serverMap), nil
}

// NewClientWithServerMap initializes whois client for library used
func NewClientWithServerMap(timeout time.Duration, serverMap DomainWhoisServerMap) *Client {
	return newClient(timeout, ianaWhoisServer, arinWhoisServer, whoisPort, serverMap, logrus.New())
}

// NewClientInternal initializes whois client for cmd and server
func NewClientInternal(timeout time.Duration, serverMap DomainWhoisServerMap, logger logrus.FieldLogger) *Client {
	return newClient(timeout, ianaWhoisServer, arinWhoisServer, whoisPort, serverMap, logger)
}

func newClient(timeout time.Duration, ianaServAddr, arinServAddr string, whoisPort int,
	whoisMap DomainWhoisServerMap, logger logrus.FieldLogger) *Client {
	c := &Client{
		dialer:       &net.Dialer{},
		ianaServAddr: ianaServAddr,
		arinServAddr: arinServAddr,
		arinMap: map[string]string{
			"APNIC":  apnicWhoisServer,
			"ARIN":   arinWhoisServer,
			"RIPE":   ripeWhoisServer,
			"LACNIC": lacnicWhoisServer,
			"AFRNIC": afrinicWhoisServer,
		},
		whoisMap:  whoisMap,
		whoisPort: whoisPort,
		timeout:   timeout,
		wtimeout:  writeTimeout,
		rtimeout:  readTimeout,
		logger:    logger,
	}
	if strings.Index(c.ianaServAddr, ":") == -1 {
		c.ianaServAddr = FmtWhoisServer(ianaServAddr, whoisPort)
	}
	if strings.Index(c.arinServAddr, ":") == -1 {
		c.arinServAddr = FmtWhoisServer(arinServAddr, whoisPort)
	}
	return c
}

func (c *Client) SetWhoisPort(port int) {
	c.whoisPort = port
}

func (c *Client) getText(ctx context.Context, dst, domain string) (string, error) {
	conn, err := c.dialer.DialContext(ctx, "tcp", dst)
	if err != nil {
		return "", fmt.Errorf("Failed to dial %s: %w", dst, err)
	}
	defer conn.Close()

	if err := conn.SetWriteDeadline(utils.UTCNow().Add(c.wtimeout)); err != nil {
		return "", fmt.Errorf("Set write deadline failed: %w", err)
	}
	if _, err = conn.Write([]byte(domain + "\r\n")); err != nil {
		return "", fmt.Errorf("Send to server failed: %w", err)
	}
	if err := conn.SetReadDeadline(utils.UTCNow().Add(c.rtimeout)); err != nil {
		return "", fmt.Errorf("Set read deadline failed: %w", err)
	}
	content, err := ioutil.ReadAll(conn)
	if err != nil {
		return "", fmt.Errorf("Read from server failed: %w", err)
	}
	return string(content), nil
}

// QueryRaw query whois server with public suffix
func (c *Client) QueryRaw(ctx context.Context, ps string, whoisServer ...string) (*Raw, error) {
	// Caller specify whois server to query
	if len(whoisServer) > 0 && len(whoisServer[0]) > 0 {
		addr := FmtWhoisServer(whoisServer[0], c.whoisPort)
		resp, err := c.getText(ctx, addr, ps)
		if err != nil {
			return NewRaw("", whoisServer[0]), err
		}
		return NewRaw(resp, whoisServer[0]), nil
	}
	// Not given whois server, search from map and query
	if wss := c.whoisMap.GetWhoisServer(ps); len(wss) > 0 {
		whoisDst := FmtWhoisServer(wss[0].Host, c.whoisPort)
		resp, err := c.getText(ctx, whoisDst, ps)
		if err != nil {
			return NewRaw("", wss[0].Host), err
		}
		if wss[0].AvailPtn != nil {
			return NewRaw(resp, wss[0].Host, wss[0].AvailPtn), nil
		}
		return NewRaw(resp, wss[0].Host), nil
	}
	return nil, errors.New("unknown whois server")
}

// Query get whois information from given whois server or predefined whois server map with domain
func (c *Client) Query(ctx context.Context, domain string, whoisServer ...string) (*wd.Whois, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	domain, err := utils.GetHost(domain)
	if err != nil {
		return nil, err
	}
	pslist, err := utils.GetPublicSuffixs(domain)
	if err != nil && len(pslist) == 0 {
		return nil, err
	}
	return c.QueryPublicSuffixs(ctx, pslist, whoisServer...)
}

// QueryPublicSuffix get whois information from given whois server or predefined whois server map with public suffix
func (c *Client) QueryPublicSuffix(ctx context.Context, ps string, whoisServer ...string) (*wd.Whois, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var wrt *Raw
	var err error
	if wrt, err = c.QueryRaw(ctx, ps, whoisServer...); err != nil {
		return nil, err
	}
	return c.Parse(ps, wrt)
}

// QueryPublicSuffixs get whois information from given whois server or predefined whois server map with public suffix list
func (c *Client) QueryPublicSuffixs(ctx context.Context, pslist []string, whoisServer ...string) (*wd.Whois, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var wrt *Raw
	var err error
	var foundPS string
	var isAvail *bool
	for _, ps := range pslist {
		if wrt, err = c.QueryRaw(ctx, ps, whoisServer...); err == nil {
			foundPS = ps
			if wrt.Avail != nil {
				isAvail = wrt.Avail
			}
			break
		}
		if wrt != nil {
			c.logger.WithFields(logrus.Fields{"ps": ps, "whois_server": wrt.Server}).WithError(err).Warn("query WHOIS")
		} else {
			c.logger.WithField("ps", ps).WithError(err).Warn("query WHOIS")
		}
	}

	if err != nil {
		if utils.IsTimeout(err) {
			return nil, ErrTimeout
		}
		return nil, err
	}
	w, err := c.Parse(foundPS, wrt)
	if err != nil {
		return w, err
	}
	if isAvail != nil {
		if w.IsAvailable = isAvail; *w.IsAvailable {
			// is available = WHOIS not found
			return w, ErrDomainIPNotFound
		}
	}
	if wd.WhoisNotFound(w.RawText) {
		return w, ErrDomainIPNotFound
	}
	return w, nil
}

// Parse get parser based on TLD and use it to parse rawtext. Also check if rawtext contains **not found** keywords
func (c *Client) Parse(ps string, wrt *Raw) (*wd.Whois, error) {
	tld := utils.GetTLD(ps)
	parser := wd.NewTLDDomainParser(tld)
	c.logger.WithFields(logrus.Fields{"tld": tld, "parser": parser.GetName()}).Info("parse")

	parsedWhois, err := parser.GetParsedWhois(wrt.Rawtext)
	if err != nil {
		return nil, err
	}
	return wd.NewWhois(parsedWhois, wrt.Rawtext, wrt.Server), nil
}

// QueryPublicSuffixsAsync performs async query and returns channel for caller to wait for the result
func (c *Client) QueryPublicSuffixsAsync(status *AsyncStatus) chan *wd.Whois {
	result := make(chan *wd.Whois)
	go func() {
		whoisStruct, err := c.QueryPublicSuffixs(context.Background(), status.PublicSuffixs, status.WhoisServer)
		if err != nil {
			status.Err = err
			if errors.Is(err, ErrDomainIPNotFound) {
				// get whois value while raw text contains keyword that represent WHOIS not found
				status.RespType = RespTypeNotFound
				result <- whoisStruct
				return
			}
			if errors.Is(err, ErrTimeout) {
				status.RespType = RespTypeTimeout
			} else {
				status.RespType = RespTypeError
			}
			close(result)
			return
		}
		status.RespType = RespTypeFound
		result <- whoisStruct
	}()
	return result
}

// QueryIPRaw query whois server with IP
func (c *Client) QueryIPRaw(ctx context.Context, ip, whoisServer string) (*Raw, error) {
	whoisDst := FmtWhoisServer(whoisServer, c.whoisPort)
	rawtext, err := c.getText(ctx, whoisDst, ip)
	if err != nil {
		return NewRaw("", whoisServer), err
	}
	return NewRaw(rawtext, whoisServer), nil
}

// ParseIP get parser and parse rawtext
func (c *Client) ParseIP(ip string, wrt *Raw) (*wip.Whois, error) {
	parser := wip.NewParser(ip, c.logger)
	parsedWhois, err := parser.Do(wrt.Rawtext)
	if err != nil {
		return nil, err
	}
	w := wip.NewWhois(parsedWhois, wrt.Rawtext, wrt.Server)
	if wip.WhoisNotFound(wrt.Rawtext) {
		return w, ErrDomainIPNotFound
	}
	return w, nil
}

// QueryIP get whois information from given whois server or query 'whois.arin.net' and parse 'OrgId'
// to get the organization and map to the whois server, query again if it's not 'whois.arin.net'
func (c *Client) QueryIP(ctx context.Context, ip string, whoisServers ...string) (*wip.Whois, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var wrt *Raw
	var orgid string
	var err error
	if len(whoisServers) > 0 && len(whoisServers[0]) > 0 {
		if wrt, err = c.QueryIPRaw(ctx, ip, whoisServers[0]); err != nil {
			if utils.IsTimeout(err) {
				return nil, ErrTimeout
			}
			return nil, fmt.Errorf("get whois error: %w", err)
		}
	} else {
		rawtext, err := c.getText(ctx, c.arinServAddr, "n "+ip)
		if err != nil {
			if utils.IsTimeout(err) {
				return nil, ErrTimeout
			}
			return nil, err
		}
		orgid = wd.FoundByKey("OrgId", rawtext)
		if ws, ok := c.arinMap[orgid]; ok {
			if wrt, err = c.QueryIPRaw(ctx, ip, ws); err != nil {
				if utils.IsTimeout(err) {
					return nil, ErrTimeout
				}
				return nil, fmt.Errorf("get whois error: %w", err)
			}
		} else {
			wrt = NewRaw(rawtext, c.arinServAddr[:strings.Index(c.arinServAddr, ":")])
		}
	}
	return c.ParseIP(ip, wrt)
}

// QueryIPAsync performs async query and returns channel for caller to wait for the result
func (c *Client) QueryIPAsync(status *AsyncStatus) chan *wip.Whois {
	result := make(chan *wip.Whois)
	go func() {
		whoisStruct, err := c.QueryIP(context.Background(), status.DomainOrIP, status.WhoisServer)
		if err != nil {
			status.Err = err
			if errors.Is(err, ErrDomainIPNotFound) {
				// get whois value while raw text contains keyword that represent WHOIS not found
				status.RespType = RespTypeNotFound
				result <- whoisStruct
				return
			}
			if errors.Is(err, ErrTimeout) {
				status.RespType = RespTypeTimeout
			} else {
				status.RespType = RespTypeError
			}
			close(result)
			return
		}
		status.RespType = RespTypeFound
		result <- whoisStruct
	}()
	return result
}