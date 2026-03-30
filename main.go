package main

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/caarlos0/env/v11"
	"go.uber.org/zap"
)

// hop-by-hop headers
// http://www.w3.org/Protocols/rfc2616/rfc2616-sec13.html
var hopHeaders = []string{
	"Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailers",
	"Transfer-Encoding",
	"Upgrade",
}

type RewriteTransport struct {
	Transport http.RoundTripper
}

type Config struct {
	ServerURL      string `env:"SERVER_URL" envDefault:"0.0.0.0:8080"`
	ProxyDSN       string `env:"PROXY_DSN,required"`
	TargetHost     string `env:"TARGET_HOST,required"`
	IgnoreSSL      bool   `env:"IGNORE_SSL" envDefault:"false"`
	DefaultHeaders map[string]string
}

func LoadConfig() (Config, error) {
	cfg := Config{}
	if err := env.Parse(&cfg); err != nil {
		return Config{}, err
	}

	headers := os.Getenv("DEFAULT_HEADERS")
	cfg.DefaultHeaders = make(map[string]string)
	if headers == "" {
		return cfg, nil
	}

	for _, header := range strings.Split(headers, ",") {
		parts := strings.SplitN(header, ":", 2)
		if len(parts) != 2 {
			return Config{}, fmt.Errorf("invalid DEFAULT_HEADERS entry: %q", header)
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if key == "" {
			return Config{}, fmt.Errorf("invalid DEFAULT_HEADERS entry: empty key")
		}
		cfg.DefaultHeaders[key] = value
	}

	return cfg, nil
}

func (t *RewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.Transport.RoundTrip(req)
}

func getProxyClient(proxy string, ignoreSsl bool) *http.Client {
	proxyUrl, _ := url.Parse(proxy)
	myClient := &http.Client{
		Transport: &RewriteTransport{
			&http.Transport{
				Proxy: http.ProxyURL(proxyUrl),
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: ignoreSsl,
				},
			},
		},
	}

	return myClient
}

func containsHeader(s string, list []string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func newProxyHandler(httpClient *http.Client, targetHost string, headersMap map[string]string, logger *zap.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// get path from request and append to target url
		target, _ := url.Parse(targetHost + r.URL.Path)
		if r.URL.RawQuery != "" {
			target.RawQuery = r.URL.RawQuery
		}

		reqBodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			logger.Error("read request body", zap.Error(err))
			return
		}

		logger.Debug("proxy request",
			zap.String("method", r.Method),
			zap.String("url", target.String()),
			zap.ByteString("reqBody", reqBodyBytes),
		)

		// create new request
		req, err := http.NewRequest(r.Method, target.String(), bytes.NewReader(reqBodyBytes))
		if err != nil {
			logger.Error("new request", zap.Error(err))
			return
		}

		// add default headers
		for k, v := range headersMap {
			req.Header.Set(k, v)
		}

		// copy headers from original request to new request
		for k, v := range r.Header {
			if !containsHeader(k, hopHeaders) {
				req.Header[k] = v
			}
		}

		// send request to target url
		resp, err := httpClient.Do(req)
		if err != nil {
			logger.Error("proxy request", zap.Error(err))
			return
		}
		defer func() {
			if err = resp.Body.Close(); err != nil {
				logger.Error("close response body", zap.Error(err))
			}
		}()

		// copy headers from response to original response
		for k, v := range resp.Header {
			w.Header()[k] = v
		}

		// copy status code from response to original response
		w.WriteHeader(resp.StatusCode)

		respBodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			logger.Error("read response body", zap.Error(err))
			return
		}

		logger.Debug("proxy response",
			zap.String("method", r.Method),
			zap.String("url", target.String()),
			zap.Int("status", resp.StatusCode),
			zap.ByteString("respBody", respBodyBytes),
		)

		// copy body from response to original response using io.Copy
		_, err = io.Copy(w, bytes.NewReader(respBodyBytes))
		if err != nil {
			logger.Error("copy response body", zap.Error(err))
			return
		}
	})
}

func main() {
	logger, err := zap.NewDevelopment()
	if err != nil {
		panic(err)
	}
	defer func() { _ = logger.Sync() }()

	cfg, err := LoadConfig()
	if err != nil {
		logger.Fatal("load config", zap.Error(err))
	}

	httpClient := getProxyClient(cfg.ProxyDSN, cfg.IgnoreSSL)

	//proxy all outgoing http requests to socks5 proxy
	if err := http.ListenAndServe(cfg.ServerURL, newProxyHandler(httpClient, cfg.TargetHost, cfg.DefaultHeaders, logger)); err != nil {
		logger.Fatal("listen", zap.String("addr", cfg.ServerURL), zap.Error(err))
	}
}
