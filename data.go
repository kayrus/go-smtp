package smtp

import (
	"io"
	"net"
	"time"
)

type EnhancedCode [3]int

// SMTPError specifies the error code, enhanced error code (if any) and
// message returned by the server.
type SMTPError struct {
	Code         int
	EnhancedCode EnhancedCode
	Message      string
}

// NoEnhancedCode is used to indicate that enhanced error code should not be
// included in response.
//
// Note that RFC 2034 requires an enhanced code to be included in all 2xx, 4xx
// and 5xx responses. This constant is exported for use by extensions, you
// should probably use EnhancedCodeNotSet instead.
var NoEnhancedCode = EnhancedCode{-1, -1, -1}

// EnhancedCodeNotSet is a nil value of EnhancedCode field in SMTPError, used
// to indicate that backend failed to provide enhanced status code. X.0.0 will
// be used (X is derived from error code).
var EnhancedCodeNotSet = EnhancedCode{0, 0, 0}

func (err *SMTPError) Error() string {
	return err.Message
}

func (err *SMTPError) Temporary() bool {
	return err.Code/100 == 4
}

var ErrDataTooLarge = &SMTPError{
	Code:         552,
	EnhancedCode: EnhancedCode{5, 3, 4},
	Message:      "Maximum message size exceeded",
}

var ErrDataTimeout = &SMTPError{
	Code:         451,
	EnhancedCode: EnhancedCode{4, 4, 2},
	Message:      "Timeout waiting for data from client",
}

type dataReader struct {
	c     *Conn
	state int

	limited bool
	n       int64 // Maximum bytes remaining
}

func newDataReader(c *Conn) *dataReader {
	dr := &dataReader{
		c: c,
	}

	if c.server.MaxMessageBytes > 0 {
		dr.limited = true
		dr.n = int64(c.server.MaxMessageBytes)
	}

	return dr
}

func (r *dataReader) Read(b []byte) (n int, err error) {
	if r.limited {
		if r.n <= 0 {
			return 0, ErrDataTooLarge
		}
		if int64(len(b)) > r.n {
			b = b[0:r.n]
		}
	}

	// Code below is taken from net/textproto with only one modification to
	// not rewrite CRLF -> LF.

	// Run data through a simple state machine to
	// elide leading dots and detect ending .\r\n line.
	const (
		stateBeginLine = iota // beginning of line; initial state; must be zero
		stateDot              // read . at beginning of line
		stateDotCR            // read .\r at beginning of line
		stateCR               // read \r (possibly at end of line)
		stateData             // reading data in middle of line
		stateEOF              // reached .\r\n end marker line
	)
	for n < len(b) && r.state != stateEOF {
		if r.c.server.ReadTimeout != 0 {
			err = r.c.conn.SetReadDeadline(time.Now().Add(r.c.server.ReadTimeout))
			if err != nil {
				break
			}
		}
		var c byte
		c, err = r.c.text.R.ReadByte()
		if err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			if e, ok := err.(net.Error); ok && e.Timeout() {
				r.c.server.ErrorLog.Printf(r.c, "data read timeout: %w", err)
				err = ErrDataTimeout
			}
			break
		}
		switch r.state {
		case stateBeginLine:
			if c == '.' {
				r.state = stateDot
				continue
			}
			r.state = stateData
		case stateDot:
			if c == '\r' {
				r.state = stateDotCR
				continue
			}
			if c == '\n' {
				r.state = stateEOF
				continue
			}

			r.state = stateData
		case stateDotCR:
			if c == '\n' {
				r.state = stateEOF
				continue
			}
			r.state = stateData
		case stateCR:
			if c == '\n' {
				r.state = stateBeginLine
				break
			}
			r.state = stateData
		case stateData:
			if c == '\r' {
				r.state = stateCR
			}
			if c == '\n' {
				r.state = stateBeginLine
			}
		}
		b[n] = c
		n++
	}
	if err == nil && r.state == stateEOF {
		err = io.EOF
	}

	if r.limited {
		r.n -= int64(n)
	}
	return
}
