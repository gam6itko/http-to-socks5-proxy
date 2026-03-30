package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestNewProxyHandler_requestAndResponseBodiesInLogAndProxied(t *testing.T) {
	const (
		clientPayload = "hello-from-client"
		suffix        = "-backend-suffix"
	)

	var backendGotBody []byte
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		backendGotBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("backend read body: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("X-Test", "1")
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte(string(backendGotBody) + suffix))
	}))
	t.Cleanup(backend.Close)

	core, logs := observer.New(zap.DebugLevel)
	logger := zap.New(core)

	h := newProxyHandler(backend.Client(), backend.URL, nil, logger)

	proxySrv := httptest.NewServer(h)
	t.Cleanup(proxySrv.Close)

	req, err := http.NewRequest(http.MethodPost, proxySrv.URL+"/api/x?q=1", bytes.NewReader([]byte(clientPayload)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Client", "a")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	if string(backendGotBody) != clientPayload {
		t.Fatalf("backend got body: got %q want %q", backendGotBody, clientPayload)
	}
	if string(respBody) != clientPayload+suffix {
		t.Fatalf("client got response: got %q want %q", respBody, clientPayload+suffix)
	}
	if resp.StatusCode != http.StatusTeapot {
		t.Fatalf("status: got %d want %d", resp.StatusCode, http.StatusTeapot)
	}
	if got := resp.Header.Get("X-Test"); got != "1" {
		t.Fatalf("response header X-Test: got %q want %q", got, "1")
	}

	wantURL := backend.URL + "/api/x?q=1"
	reqEntries := logs.FilterMessage("proxy request").FilterField(zap.ByteString("reqBody", []byte(clientPayload))).All()
	if len(reqEntries) != 1 {
		t.Fatalf("proxy request log entries: got %d want 1; all=%#v", len(reqEntries), logs.All())
	}
	if got := fieldString(t, reqEntries[0].Context, "url"); got != wantURL {
		t.Fatalf("proxy request url field: got %q want %q", got, wantURL)
	}

	respEntries := logs.FilterMessage("proxy response").FilterField(zap.ByteString("respBody", []byte(clientPayload+suffix))).All()
	if len(respEntries) != 1 {
		t.Fatalf("proxy response log entries: got %d want 1", len(respEntries))
	}
	if got := fieldInt64(t, respEntries[0].Context, "status"); int(got) != http.StatusTeapot {
		t.Fatalf("proxy response status field: got %d want %d", got, http.StatusTeapot)
	}
}

func TestNewProxyHandler_emptyBodiesLoggedAndProxied(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if len(b) != 0 {
			t.Errorf("backend expected empty body, got %q", b)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(backend.Close)

	core, logs := observer.New(zap.DebugLevel)
	logger := zap.New(core)

	h := newProxyHandler(backend.Client(), backend.URL, nil, logger)
	proxySrv := httptest.NewServer(h)
	t.Cleanup(proxySrv.Close)

	resp, err := http.Get(proxySrv.URL + "/empty")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 0 {
		t.Fatalf("expected empty response body, got %q", body)
	}

	reqEntries := logs.FilterMessage("proxy request").FilterField(zap.ByteString("reqBody", nil)).All()
	if len(reqEntries) != 1 {
		t.Fatalf("proxy request entries: got %d", len(reqEntries))
	}
	respEntries := logs.FilterMessage("proxy response").FilterField(zap.ByteString("respBody", nil)).All()
	if len(respEntries) != 1 {
		t.Fatalf("proxy response entries: got %d", len(respEntries))
	}
}

func fieldString(t *testing.T, fields []zapcore.Field, key string) string {
	t.Helper()
	for _, f := range fields {
		if f.Key == key {
			return f.String
		}
	}
	t.Fatalf("field %q not found", key)
	return ""
}

func fieldInt64(t *testing.T, fields []zapcore.Field, key string) int64 {
	t.Helper()
	for _, f := range fields {
		if f.Key == key {
			return f.Integer
		}
	}
	t.Fatalf("field %q not found", key)
	return 0
}
