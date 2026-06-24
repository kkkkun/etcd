// Copyright 2024 The etcd Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package embed

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"golang.org/x/net/http2"
)

func TestProbabilisticGoawayDecider(t *testing.T) {
	cases := []struct {
		name         string
		chance       float64
		nextVal      float64
		expectGoaway bool
	}{
		{
			name:         "chance zero never goaway",
			chance:       0,
			nextVal:      0,
			expectGoaway: false,
		},
		{
			name:         "chance one always goaway",
			chance:       1,
			nextVal:      0.5,
			expectGoaway: true,
		},
		{
			name:         "next less than chance triggers goaway",
			chance:       0.5,
			nextVal:      0.499,
			expectGoaway: true,
		},
		{
			name:         "next equal to chance does not trigger goaway",
			chance:       0.5,
			nextVal:      0.5,
			expectGoaway: false,
		},
		{
			name:         "next greater than chance does not trigger goaway",
			chance:       0.5,
			nextVal:      0.501,
			expectGoaway: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &probabilisticGoawayDecider{
				chance: tc.chance,
				next:   func() float64 { return tc.nextVal },
			}
			result := d.goaway(nil)
			assert.Equal(t, tc.expectGoaway, result)
		})
	}
}

func TestGoawayHandlerHTTP1NotAffected(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	h := withProbabilisticGoaway(inner, 1)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Proto = "HTTP/1.1"
	req.ProtoMajor = 1
	req.ProtoMinor = 1

	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, rec.Header().Get("Connection"))
	body, _ := io.ReadAll(rec.Body)
	assert.Equal(t, "ok", string(body))
}

func TestGoawayHandlerHTTP2Triggered(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	h := withProbabilisticGoaway(inner, 1)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Proto = "HTTP/2.0"
	req.ProtoMajor = 2
	req.ProtoMinor = 0

	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "close", rec.Header().Get("Connection"))
}

func TestGoawayHandlerHTTP2NotTriggered(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	h := withProbabilisticGoaway(inner, 0)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Proto = "HTTP/2.0"
	req.ProtoMajor = 2
	req.ProtoMinor = 0

	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, rec.Header().Get("Connection"))
}

func TestWrapWithGoAwayDisabled(t *testing.T) {
	sctx := &serveCtx{lg: zap.NewNop(), goAwayChance: 0}
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})

	wrapped := sctx.wrapWithGoAway(inner)
	assert.Same(t, inner, wrapped, "wrapWithGoAway should return the original handler when chance is 0")
}

func TestWrapWithGoAwayEnabled(t *testing.T) {
	sctx := &serveCtx{lg: zap.NewNop(), goAwayChance: 0.001}
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})

	wrapped := sctx.wrapWithGoAway(inner)
	assert.NotSame(t, inner, wrapped, "wrapWithGoAway should return a wrapped handler when chance > 0")
	_, ok := wrapped.(*goawayHandler)
	assert.True(t, ok, "wrapped handler should be a *goawayHandler")
}

func TestGoawayHTTP2NewConnectionAfterGoaway(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("hello"))
	})

	mux := http.NewServeMux()
	mux.Handle("/normal", withProbabilisticGoaway(inner, 0))
	mux.Handle("/goaway", withProbabilisticGoaway(inner, 1))

	s := httptest.NewUnstartedServer(mux)
	require.NoError(t, http2.ConfigureServer(s.Config, &http2.Server{}))
	s.TLS = s.Config.TLSConfig
	s.StartTLS()
	defer s.Close()

	var mu sync.Mutex
	var localAddrs []string

	tlsConfig := &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{http2.NextProtoTLS},
	}
	tr := &http.Transport{
		TLSClientConfig:     tlsConfig,
		MaxIdleConnsPerHost: -1,
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := tls.Dial(network, addr, tlsConfig)
			if err != nil {
				return nil, err
			}
			mu.Lock()
			localAddrs = append(localAddrs, conn.LocalAddr().String())
			mu.Unlock()
			return conn, nil
		},
	}
	require.NoError(t, http2.ConfigureTransport(tr))
	client := &http.Client{Transport: tr}

	doReq := func(url string) {
		resp, err := client.Get(s.URL + url)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		body, _ := io.ReadAll(resp.Body)
		assert.Equal(t, "hello", string(body))
	}

	doReq("/normal")
	doReq("/normal")
	doReq("/goaway")
	doReq("/normal")

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 2, len(localAddrs), "expected 2 TCP connections: 1 initial + 1 after GOAWAY")
}

func TestGoawayHTTP1RequestsNotAffected(t *testing.T) {
	s := httptest.NewUnstartedServer(withProbabilisticGoaway(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("hello"))
	}), 1))
	require.NoError(t, http2.ConfigureServer(s.Config, &http2.Server{}))
	s.TLS = s.Config.TLSConfig
	s.StartTLS()
	defer s.Close()

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
				NextProtos:         []string{"http/1.1"},
			},
		},
	}

	resp, err := client.Get(s.URL)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Empty(t, resp.Header.Get("Connection"), "HTTP/1.1 requests should not get Connection: close")
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, "hello", string(body))
}

func TestGoawayValidateConfig(t *testing.T) {
	cases := []struct {
		name        string
		chance      float64
		expectError bool
	}{
		{name: "zero is valid", chance: 0, expectError: false},
		{name: "small chance is valid", chance: 0.001, expectError: false},
		{name: "half is valid", chance: 0.5, expectError: false},
		{name: "one is valid", chance: 1, expectError: false},
		{name: "negative is invalid", chance: -0.1, expectError: true},
		{name: "greater than one is invalid", chance: 1.1, expectError: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := NewConfig()
			cfg.Logger = "zap"
			cfg.LogOutputs = []string{"/dev/null"}
			cfg.GoAwayChance = tc.chance

			err := cfg.Validate()
			if tc.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "goaway-chance")
			}
		})
	}
}
