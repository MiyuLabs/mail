package imap

import (
	"bufio"
	"bytes"
	"fmt"
	"testing"

	goimap "github.com/emersion/go-imap"
)

const (
	maxFuzzResponseBytes = 64 << 10
	maxFuzzLiteralBytes  = 16 << 10
)

// readIMAPResponse uses the same response reader as the go-imap client wrapped
// by this package, with a bounded literal size for untrusted input.
func readIMAPResponse(data []byte) (goimap.Resp, error) {
	reader := goimap.NewReader(bufio.NewReader(bytes.NewReader(data)))
	reader.MaxLiteralSize = maxFuzzLiteralBytes
	return goimap.ReadResp(reader)
}

func FuzzIMAPResponseReader(f *testing.F) {
	seeds := []string{
		"A001 OK [UIDVALIDITY 123] command complete\r\n",
		"* OK IMAP4rev1 server ready\r\n",
		"* 12 EXISTS\r\n",
		"* OK [UIDNEXT 124] next UID\r\n",
		"* 3 FETCH (UID 42 FLAGS (\\Seen \\Flagged))\r\n",
		"* 1 FETCH (UID 9 BODY[] {4}\r\ndata)\r\n",
		"* 1 FETCH (BODY[HEADER] {12}\r\nSubject: hi\r\n)\r\n",
		"A002 OK [CAPABILITY IMAP4rev1 UIDPLUS] capabilities\r\n",
		"A003 OK \"quoted \\\"string\\\"\" complete\r\n",
		"* X (A (B (C)))\r\n",
		"* 1 FETCH (BODY[] {0}\r\n)\r\n",
		"* 1 FETCH (BODY[] {4294967295}\r\n",
		"* 1 FETCH (BODY[] {999999999999999999999}\r\n",
		"* 1 FETCH (BODY[] {6}\r\nshort)\r\n",
		"* X \"unterminated\r\n",
		"* X \"invalid\\qescape\"\r\n",
		"* 1 FETCH (FLAGS (\\Seen)\r\n",
		"* 1 FETCH (UID nope FLAGS (\\Seen))\r\n",
		"* 1 FETCH (BODY[] {x}\r\n)\r\n",
		"* 1 FETCH (BODY[] {1+}\r\nx)\r\n",
		"*\r\n",
		"",
	}
	for _, seed := range seeds {
		f.Add([]byte(seed))
	}
	f.Add([]byte{'*', ' ', 0xff, '\r', '\n'})

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > maxFuzzResponseBytes {
			return
		}

		resp, err := readIMAPResponse(data)
		if err != nil {
			return
		}
		if resp == nil {
			t.Fatal("response reader returned neither a response nor an error")
		}
		switch resp.(type) {
		case *goimap.DataResp, *goimap.StatusResp, *goimap.ContinuationReq:
			// These are the complete set of response types produced by ReadResp.
		default:
			t.Fatalf("response reader returned unexpected type %T", resp)
		}
	})
}

func TestIMAPResponseReaderValidAndMalformedSeeds(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantType   string
		wantName   string
		wantFields int
		wantError  bool
	}{
		{name: "tagged status", input: "A001 OK [UIDVALIDITY 123] command complete\r\n", wantType: "*imap.StatusResp"},
		{name: "untagged data", input: "* 12 EXISTS\r\n", wantType: "*imap.DataResp", wantName: "EXISTS", wantFields: 2},
		{name: "uid next status code", input: "* OK [UIDNEXT 124] next UID\r\n", wantType: "*imap.StatusResp"},
		{name: "fetch flags uid", input: "* 3 FETCH (UID 42 FLAGS (\\Seen \\Flagged))\r\n", wantType: "*imap.DataResp", wantName: "FETCH", wantFields: 3},
		{name: "fetch body literal", input: "* 1 FETCH (UID 9 BODY[] {4}\r\ndata)\r\n", wantType: "*imap.DataResp", wantName: "FETCH", wantFields: 3},
		{name: "nested response code", input: "A002 OK [CAPABILITY IMAP4rev1 UIDPLUS] capabilities\r\n", wantType: "*imap.StatusResp"},
		{name: "truncated literal", input: "* 1 FETCH (BODY[] {6}\r\nshort)\r\n", wantError: true},
		{name: "overflow literal length", input: "* 1 FETCH (BODY[] {4294967295}\r\n", wantError: true},
		{name: "non-numeric literal length", input: "* 1 FETCH (BODY[] {x}\r\n)\r\n", wantError: true},
		{name: "malformed quoted string", input: "* X \"unterminated\r\n", wantError: true},
		{name: "invalid quoted escape", input: "* X \"invalid\\qescape\"\r\n", wantError: true},
		{name: "unclosed nested list", input: "* 1 FETCH (FLAGS (\\Seen)\r\n", wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := readIMAPResponse([]byte(tt.input))
			if tt.wantError {
				if err == nil || resp != nil {
					t.Fatalf("readIMAPResponse() = (%v, %v), want (nil, error)", resp, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("readIMAPResponse() error = %v", err)
			}
			if typ := fmt.Sprintf("%T", resp); typ != tt.wantType {
				t.Fatalf("response type = %s, want %s", typ, tt.wantType)
			}
			if dataResp, ok := resp.(*goimap.DataResp); ok && len(dataResp.Fields) != tt.wantFields {
				t.Errorf("data fields = %#v, want %d fields", dataResp.Fields, tt.wantFields)
			}
			if tt.wantName != "" {
				name, _, ok := goimap.ParseNamedResp(resp)
				if !ok || name != tt.wantName {
					t.Errorf("response name = %q (ok=%v), want %q", name, ok, tt.wantName)
				}
			}
		})
	}
}
