// This code is available on the terms of the project LICENSE.md file,
// also available online at https://blueoakcouncil.org/license/1.0.0.

package admin

import (
	"context"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	dexsrv "decred.org/dcrdex/server/dex"
	"github.com/decred/dcrd/certgen"
	"github.com/decred/slog"
)

func init() {
	log = slog.NewBackend(os.Stdout).Logger("TEST")
	log.SetLevel(slog.LevelTrace)
}

var (
	// Check that *dexsrv.DEX satisfies SvrCore.
	_ SvrCore = (*dexsrv.DEX)(nil)
)

type TCore struct{}

func (c *TCore) ConfigMsg() json.RawMessage { return nil }

type tResponseWriter struct {
	b    []byte
	code int
}

func (w *tResponseWriter) Header() http.Header {
	return make(http.Header)
}
func (w *tResponseWriter) Write(msg []byte) (int, error) {
	w.b = msg
	return len(msg), nil
}
func (w *tResponseWriter) WriteHeader(statusCode int) {
	w.code = statusCode
}

// genCertPair generates a key/cert pair to the paths provided.
func genCertPair(certFile, keyFile string) error {
	log.Infof("Generating TLS certificates...")

	org := "dcrdex autogenerated cert"
	validUntil := time.Now().Add(10 * 365 * 24 * time.Hour)
	cert, key, err := certgen.NewTLSCertPair(elliptic.P521(), org,
		validUntil, nil)
	if err != nil {
		return err
	}

	// Write cert and key files.
	if err = ioutil.WriteFile(certFile, cert, 0644); err != nil {
		return err
	}
	if err = ioutil.WriteFile(keyFile, key, 0600); err != nil {
		os.Remove(certFile)
		return err
	}

	log.Infof("Done generating TLS certificates")
	return nil
}

var tPort = 5555

// If start is true, the Server's Run goroutine is started, and the shutdown
// func must be called when finished with the Server.
func newTServer(t *testing.T, start bool, authSHA [32]byte) (*Server, func()) {
	tmp, err := ioutil.TempDir("", "admin")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmp)

	cert, key := filepath.Join(tmp, "tls.cert"), filepath.Join(tmp, "tls.key")
	err = genCertPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}

	s, err := NewServer(&SrvConfig{
		Core:    new(TCore),
		Addr:    fmt.Sprintf("localhost:%d", tPort),
		Cert:    cert,
		Key:     key,
		AuthSHA: authSHA,
	})
	if err != nil {
		t.Fatalf("error creating Server: %v", err)
	}
	if !start {
		return s, func() {}
	}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		s.Run(ctx)
		wg.Done()
	}()
	shutdown := func() {
		cancel()
		wg.Wait()
	}
	return s, shutdown
}

func TestPing(t *testing.T) {
	w := httptest.NewRecorder()
	// apiPing is a Server method, but the receiver and http.Request are unused.
	(*Server)(nil).apiPing(w, nil)
	if w.Code != 200 {
		t.Fatalf("apiPing returned code %d, expected 200", w.Code)
	}

	resp := w.Result()
	ctHdr := resp.Header.Get("Content-Type")
	wantCt := "application/json; charset=utf-8"
	if ctHdr != wantCt {
		t.Errorf("Content-Type incorrect. got %q, expected %q", ctHdr, wantCt)
	}

	// JSON strings are double quoted. Each value is terminated with a newline.
	expectedBody := `"pong"` + "\n"
	if w.Body == nil {
		t.Fatalf("got empty body")
	}
	gotBody := w.Body.String()
	if gotBody != expectedBody {
		t.Errorf("apiPong response said %q, expected %q", gotBody, expectedBody)
	}
}

func TestAuthMiddleware(t *testing.T) {
	pass := "password123"
	authSHA := sha256.Sum256([]byte(pass))
	s, _ := newTServer(t, false, authSHA)
	am := s.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	r, _ := http.NewRequest("GET", "", nil)
	r.RemoteAddr = "localhost"

	wantAuthError := func(name string, want bool) {
		w := &tResponseWriter{}
		am.ServeHTTP(w, r)
		if w.code != http.StatusUnauthorized && w.code != http.StatusOK {
			t.Fatalf("unexpected HTTP error %d for test \"%s\"", w.code, name)
		}
		switch want {
		case true:
			if w.code != http.StatusUnauthorized {
				t.Fatalf("Expected unauthorized HTTP error for test \"%s\"", name)
			}
		case false:
			if w.code != http.StatusOK {
				t.Fatalf("Expected OK HTTP status for test \"%s\"", name)
			}
		}
	}

	tests := []struct {
		name, user, pass string
		wantErr          bool
	}{{
		name: "user and correct password",
		user: "user",
		pass: pass,
	}, {
		name: "only correct password",
		pass: pass,
	}, {
		name:    "only user",
		user:    "user",
		wantErr: true,
	}, {
		name:    "no user or password",
		wantErr: true,
	}, {
		name:    "wrong password",
		user:    "user",
		pass:    pass[1:],
		wantErr: true,
	}}
	for _, test := range tests {
		r.SetBasicAuth(test.user, test.pass)
		wantAuthError(test.name, test.wantErr)
	}
}
