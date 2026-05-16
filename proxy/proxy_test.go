package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gotest.tools/v3/assert"

	"github.com/icholy/mcproxy/config"
	"github.com/icholy/mcproxy/mcpx"
	"github.com/icholy/mcproxy/provider"
)

// startUpstream returns an httptest server hosting an mcp.Server with a
// single "ping" tool.
func startUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "fake-upstream", Version: "0"}, nil)
	srv.AddTool(&mcp.Tool{
		Name:        "ping",
		Description: "returns pong",
		InputSchema: map[string]any{"type": "object"},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "pong"}}}, nil
	})
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))
	t.Cleanup(ts.Close)
	return ts
}

// startProxy boots a Proxy that points at upstreamURL and serves it via
// an httptest server, returning the proxy's URL. It blocks until the
// upstream session is open or 5s elapse.
func startProxy(t *testing.T, upstreamURL string) string {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "mcproxy.json")
	body := `{
        "proxy": {"transport": "streamable"},
        "providers": [],
        "servers": {
            "fake": {"transport": "streamable", "url": "` + upstreamURL + `"}
        }
    }`
	assert.NilError(t, os.WriteFile(cfgPath, []byte(body), 0o600))

	cfg, err := config.Load(cfgPath)
	assert.NilError(t, err)

	pr, err := New(cfg, provider.Providers{}, provider.NewBus())
	assert.NilError(t, err)

	done := make(chan struct{})
	go func() {
		_ = pr.Run(t.Context())
		close(done)
	}()
	t.Cleanup(func() { <-done })

	ts := httptest.NewServer(pr)
	t.Cleanup(ts.Close)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := pr.upstreams["fake"].Session(); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return ts.URL
}

func TestProxy_ListTools(t *testing.T) {
	up := startUpstream(t)
	proxyURL := startProxy(t, up.URL)

	c := mcp.NewClient(&mcp.Implementation{Name: "client", Version: "0"}, nil)
	sess, err := c.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: proxyURL}, nil)
	assert.NilError(t, err)
	defer sess.Close()

	res, err := sess.ListTools(t.Context(), &mcp.ListToolsParams{})
	assert.NilError(t, err)
	assert.Equal(t, len(res.Tools), 1)
	assert.Equal(t, res.Tools[0].Name, "fake"+mcpx.NameSeparator+"ping")
}
