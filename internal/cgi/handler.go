package cgi

import (
	"context"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
)

// RunCGI runs the CGI handler that proxies requests to the Unix socket server.
// It uses Go's standard library net/http/cgi and net/http/httputil packages.
func RunCGI(socketPath string) {
	// If the specified socket does not exist, probe common fnOS locations
	if socketPath == "" || !isSocket(socketPath) {
		candidates := []string{
			"/tmp/fn-docker-to-desktop.sock",
			"/var/apps/fn-docker-to-desktop/target/app.sock",
			"/usr/local/apps/@appcenter/fn-docker-to-desktop/app.sock",
		}
		for _, c := range candidates {
			if isSocket(c) {
				socketPath = c
				break
			}
		}
	}

	// Create reverse proxy to Unix socket
	proxy := newUnixSocketProxy(socketPath)

	// Use standard CGI handler
	_ = cgi.Serve(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Strip CGI prefix from path
		// e.g. /cgi/ThirdParty/fndocker.example/index.cgi/redirect/fndocker.example/_
		// We need: /redirect/fndocker.example/_
		path := r.URL.Path
		if idx := strings.Index(path, "index.cgi"); idx != -1 {
			path = path[idx+len("index.cgi"):]
			if path == "" {
				path = "/"
			}
		}
		r.URL.Path = path
		r.RequestURI = path
		if r.URL.RawQuery != "" {
			r.RequestURI = path + "?" + r.URL.RawQuery
		}

		// Forward to Unix socket server
		proxy.ServeHTTP(w, r)
	}))
}

func isSocket(path string) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeSocket != 0 || fi.Mode().IsRegular()
}

// newUnixSocketProxy creates a reverse proxy that connects to a Unix domain socket.
func newUnixSocketProxy(socketPath string) *httputil.ReverseProxy {
	target, _ := url.Parse("http://localhost")
	proxy := httputil.NewSingleHostReverseProxy(target)

	proxy.Transport = &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return net.Dial("unix", socketPath)
		},
	}

	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(serviceUnavailableHTML))
	}

	return proxy
}

const serviceUnavailableHTML = `<!DOCTYPE html>
<html>
<head>
    <meta charset="utf-8">
    <title>服务未启动</title>
    <style>
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
            display: flex;
            justify-content: center;
            align-items: center;
            height: 100vh;
            margin: 0;
            background: #f8fafc;
            color: #334155;
        }
        .container {
            text-align: center;
            padding: 2.5rem;
            background: white;
            border-radius: 12px;
            box-shadow: 0 4px 15px rgba(0,0,0,0.08);
            max-width: 420px;
        }
        h1 { color: #e11d48; margin-top: 0; font-size: 1.5rem; }
        p { margin: 0.5rem 0; line-height: 1.5; font-size: 0.95rem; }
    </style>
</head>
<body>
    <div class="container">
        <h1>把 Docker 放到桌面 服务未就绪</h1>
        <p>后台守护进程当前未处于运行状态。</p>
        <p>请在飞牛应用中心确认「把 Docker 放到桌面」已启用。</p>
    </div>
</body>
</html>`
