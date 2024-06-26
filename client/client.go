// Package nntpclient provides an NNTP Client.
package nntpclient

import (
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/textproto"
	"strconv"
	"strings"

	"github.com/yannik995/go-nntp"
)

// Client is an NNTP client.
type Client struct {
	conn         *textproto.Conn
	netconn      net.Conn
	tls          bool
	Banner       string
	capabilities []string
}

// New connects a client to an NNTP server.
func New(network, addr string) (*Client, error) {
	netconn, err := net.Dial(network, addr)
	if err != nil {
		return nil, err
	}
	return connect(netconn)
}

// NewConn wraps an existing connection, for example one opened with tls.Dial
func NewConn(netconn net.Conn) (*Client, error) {
	client, err := connect(netconn)
	if err != nil {
		return nil, err
	}
	if _, ok := netconn.(*tls.Conn); ok {
		client.tls = true
	}
	return client, nil
}

// NewTLS connects to an NNTP server over a dedicated TLS port like 563
func NewTLS(network, addr string, config *tls.Config) (*Client, error) {
	netconn, err := tls.Dial(network, addr, config)
	if err != nil {
		return nil, err
	}
	client, err := connect(netconn)
	if err != nil {
		return nil, err
	}
	client.tls = true
	return client, nil
}

func connect(netconn net.Conn) (*Client, error) {
	conn := textproto.NewConn(netconn)
	_, msg, err := conn.ReadCodeLine(20)
	if err != nil {
		return nil, err
	}
	return &Client{
		conn:    conn,
		netconn: netconn,
		Banner:  msg,
	}, nil
}

// Close this client.
func (c *Client) Close() error {
	return c.conn.Close()
}

// Authenticate against an NNTP server using authinfo user/pass
func (c *Client) Authenticate(user, pass string) (msg string, err error) {
	err = c.conn.PrintfLine("authinfo user %s", user)
	if err != nil {
		return
	}
	_, _, err = c.conn.ReadCodeLine(381)
	if err != nil {
		return
	}

	err = c.conn.PrintfLine("authinfo pass %s", pass)
	if err != nil {
		return
	}
	_, msg, err = c.conn.ReadCodeLine(281)
	return
}

func parsePosting(p string) nntp.PostingStatus {
	switch p {
	case "y":
		return nntp.PostingPermitted
	case "m":
		return nntp.PostingModerated
	}
	return nntp.PostingNotPermitted
}

// List groups
func (c *Client) List(sub string) (rv []nntp.Group, err error) {
	_, _, err = c.Command("LIST "+sub, 215)
	if err != nil {
		return
	}
	var groupLines []string
	groupLines, err = c.conn.ReadDotLines()
	if err != nil {
		return
	}
	rv = make([]nntp.Group, 0, len(groupLines))
	for _, l := range groupLines {
		parts := strings.Split(l, " ")
		high, errh := strconv.ParseInt(parts[1], 10, 64)
		low, errl := strconv.ParseInt(parts[2], 10, 64)
		if errh == nil && errl == nil {
			rv = append(rv, nntp.Group{
				Name:    parts[0],
				High:    high,
				Low:     low,
				Posting: parsePosting(parts[3]),
			})
		}
	}
	return
}

// Group selects a group.
func (c *Client) Group(name string) (rv nntp.Group, err error) {
	var msg string
	_, msg, err = c.Command("GROUP "+name, 211)
	if err != nil {
		return
	}
	// count first last name
	parts := strings.Split(msg, " ")
	if len(parts) != 4 {
		err = errors.New("Don't know how to parse result: " + msg)
	}
	rv.Count, err = strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return
	}
	rv.Low, err = strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return
	}
	rv.High, err = strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return
	}
	rv.Name = parts[3]

	return
}

// Article grabs an article
func (c *Client) Article(specifier string) (int64, string, io.Reader, error) {
	err := c.conn.PrintfLine("ARTICLE %s", specifier)
	if err != nil {
		return 0, "", nil, err
	}
	return c.articleish(220)
}

// Head gets the headers for an article
func (c *Client) Head(specifier string) (int64, string, io.Reader, error) {
	err := c.conn.PrintfLine("HEAD %s", specifier)
	if err != nil {
		return 0, "", nil, err
	}
	return c.articleish(221)
}

// Body gets the body of an article
func (c *Client) Body(specifier string) (int64, string, io.Reader, error) {
	err := c.conn.PrintfLine("BODY %s", specifier)
	if err != nil {
		return 0, "", nil, err
	}
	return c.articleish(222)
}

func (c *Client) articleish(expected int) (int64, string, io.Reader, error) {
	_, msg, err := c.conn.ReadCodeLine(expected)
	if err != nil {
		return 0, "", nil, err
	}
	parts := strings.SplitN(msg, " ", 2)
	n, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, "", nil, err
	}
	return n, parts[1], c.conn.DotReader(), nil
}

// Post a new article
//
// The reader should contain the entire article, headers and body in
// RFC822ish format.
func (c *Client) Post(r io.Reader) error {
	err := c.conn.PrintfLine("POST")
	if err != nil {
		return err
	}
	_, _, err = c.conn.ReadCodeLine(340)
	if err != nil {
		return err
	}
	w := c.conn.DotWriter()
	_, err = io.Copy(w, r)
	if err != nil {
		// This seems really bad
		return err
	}
	w.Close()
	_, _, err = c.conn.ReadCodeLine(240)
	return err
}

// Command sends a low-level command and get a response.
//
// This will return an error if the code doesn't match the expectCode
// prefix.  For example, if you specify "200", the response code MUST
// be 200 or you'll get an error.  If you specify "2", any code from
// 200 (inclusive) to 300 (exclusive) will be success.  An expectCode
// of -1 disables this behavior.
func (c *Client) Command(cmd string, expectCode int) (int, string, error) {
	err := c.conn.PrintfLine(cmd)
	if err != nil {
		return 0, "", err
	}
	return c.conn.ReadCodeLine(expectCode)
}

// asLines issues a command and returns the response's data block as lines.
func (c *Client) asLines(cmd string, expectCode int) ([]string, error) {
	_, _, err := c.Command(cmd, expectCode)
	if err != nil {
		return nil, err
	}
	return c.conn.ReadDotLines()
}

// Capabilities retrieves a list of supported capabilities.
//
// See https://datatracker.ietf.org/doc/html/rfc3977#section-5.2.2
func (c *Client) Capabilities() ([]string, error) {
	caps, err := c.asLines("CAPABILITIES", 101)
	if err != nil {
		return nil, err
	}
	for i, line := range caps {
		caps[i] = strings.ToUpper(line)
	}
	c.capabilities = caps
	return caps, nil
}

// GetCapability returns a complete capability line.
//
// "Each capability line consists of one or more tokens, which MUST be
// separated by one or more space or TAB characters."
//
// From https://datatracker.ietf.org/doc/html/rfc3977#section-3.3.1
func (c *Client) GetCapability(capability string) string {
	capability = strings.ToUpper(capability)
	for _, capa := range c.capabilities {
		i := strings.IndexAny(capa, "\t ")
		if i != -1 && capa[:i] == capability {
			return capa
		}
		if capa == capability {
			return capa
		}
	}
	return ""
}

// HasCapabilityArgument indicates whether a capability arg is supported.
//
// Here, "argument" means any token after the label in a capabilities response
// line. Some, like "ACTIVE" in "LIST ACTIVE", are not command arguments but
// rather "keyword" components of compound commands called "variants."
//
// See https://datatracker.ietf.org/doc/html/rfc3977#section-9.5
func (c *Client) HasCapabilityArgument(
	capability, argument string,
) (bool, error) {
	if c.capabilities == nil {
		return false, errors.New("Capabilities unpopulated")
	}
	capLine := c.GetCapability(capability)
	if capLine == "" {
		return false, errors.New("No such capability")
	}
	argument = strings.ToUpper(argument)
	for _, capArg := range strings.Fields(capLine)[1:] {
		if capArg == argument {
			return true, nil
		}
	}
	return false, nil
}

// ListOverviewFmt performs a LIST OVERVIEW.FMT query.
//
// According to the spec, the presence of an "OVER" line in the capabilities
// response means this LIST variant is supported, so there's no reason to
// check for it among the keywords in the "LIST" line, strictly speaking.
//
// See https://datatracker.ietf.org/doc/html/rfc3977#section-3.3.2
func (c *Client) ListOverviewFmt() ([]string, error) {
	fields, err := c.asLines("LIST OVERVIEW.FMT", 215)
	if err != nil {
		return nil, err
	}
	return fields, nil
}

// Over returns a list of raw overview lines with tab-separated fields.
func (c *Client) Over(specifier string) ([]string, error) {
	lines, err := c.asLines("OVER "+specifier, 224)
	if err != nil {
		return nil, err
	}
	return lines, nil
}

func (c *Client) HasTLS() bool {
	return c.tls
}

// StartTLS sends the STARTTLS command and refreshes capabilities.
//
// See https://datatracker.ietf.org/doc/html/rfc4642 and net/smtp.go, from
// which this was adapted, and maybe NNTP.startls in Python's nntplib also.
func (c *Client) StartTLS(config *tls.Config) error {
	if c.tls {
		return errors.New("TLS already active")
	}
	_, _, err := c.Command("STARTTLS", 382)
	if err != nil {
		return err
	}
	c.netconn = tls.Client(c.netconn, config)
	c.conn = textproto.NewConn(c.netconn)
	c.tls = true
	_, err = c.Capabilities()
	if err != nil {
		return err
	}
	return nil
}
