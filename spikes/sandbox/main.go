// Spike E: sandbox evaluation helpers.
//
// Throwaway code. One static binary with two modes, run inside rootless Podman
// containers built FROM scratch (see run-podman.sh):
//
//	egress proxy -listen :3128 -allow example.com   allowlist HTTP CONNECT proxy
//	egress probe                                    reports what a worker can reach
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: egress proxy|probe [flags]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "proxy":
		fs := flag.NewFlagSet("proxy", flag.ExitOnError)
		listen := fs.String("listen", ":3128", "listen address")
		allow := fs.String("allow", "", "comma-separated allowed hosts (exact, or .suffix)")
		_ = fs.Parse(os.Args[2:])
		runProxy(*listen, strings.Split(*allow, ","))
	case "probe":
		runProbe()
	default:
		os.Exit(2)
	}
}

func allowed(host string, list []string) bool {
	for _, a := range list {
		a = strings.TrimSpace(strings.ToLower(a))
		if a == "" {
			continue
		}
		if host == a || (strings.HasPrefix(a, ".") && strings.HasSuffix(host, a)) {
			return true
		}
	}
	return false
}

// runProxy accepts only CONNECT to port 443 of allowlisted hosts. Plain HTTP is
// refused, so every allowed request is TLS end-to-end and the proxy never sees
// request contents.
func runProxy(listen string, allow []string) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, port, err := net.SplitHostPort(r.Host)
		if r.Method != http.MethodConnect || err != nil || port != "443" || !allowed(strings.ToLower(host), allow) {
			slog.Info("deny", "method", r.Method, "host", r.Host)
			http.Error(w, "egress denied", http.StatusForbidden)
			return
		}
		up, err := net.DialTimeout("tcp", r.Host, 5*time.Second)
		if err != nil {
			slog.Warn("upstream dial failed", "host", r.Host, "err", err)
			http.Error(w, "upstream", http.StatusBadGateway)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			up.Close()
			return
		}
		w.WriteHeader(http.StatusOK)
		client, buf, err := hj.Hijack()
		if err != nil {
			up.Close()
			return
		}
		slog.Info("allow", "host", r.Host)
		go func() { _, _ = io.Copy(up, buf); up.Close() }()
		_, _ = io.Copy(client, up)
		client.Close()
	})
	slog.Info("proxy listening", "addr", listen, "allow", allow)
	if err := http.ListenAndServe(listen, h); err != nil {
		slog.Error("proxy", "err", err)
		os.Exit(1)
	}
}

func check(name string, ok bool, detail string) {
	status := "BLOCKED/NO"
	if ok {
		status = "OK/YES"
	}
	fmt.Printf("%-44s %-10s %s\n", name, status, detail)
}

func runProbe() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	d := net.Dialer{Timeout: 3 * time.Second}

	_, err := d.DialContext(ctx, "tcp", "example.com:443")
	check("direct TCP to example.com:443", err == nil, errStr(err))
	_, err = d.DialContext(ctx, "tcp", "1.1.1.1:443")
	check("direct TCP to 1.1.1.1:443 (no DNS)", err == nil, errStr(err))
	_, err = net.DefaultResolver.LookupHost(ctx, "example.com")
	check("DNS lookup example.com", err == nil, errStr(err))

	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}}
	for _, u := range []string{"https://example.com/", "https://example.org/"} {
		resp, err := client.Get(u)
		detail := errStr(err)
		if err == nil {
			detail = resp.Status
			resp.Body.Close()
		}
		check("HTTPS via proxy "+u, err == nil && resp.StatusCode == 200, detail)
	}

	err = os.WriteFile("/workspace/probe.txt", []byte("written by probe\n"), 0o644)
	check("write /workspace/probe.txt", err == nil, errStr(err))
	err = os.WriteFile("/probe-root.txt", []byte("x"), 0o644)
	check("write / (rootfs)", err == nil, errStr(err))
	_, err = os.Stat("/home")
	check("host /home visible", err == nil, errStr(err))
	entries, _ := os.ReadDir("/")
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	fmt.Printf("%-44s %s\n", "rootfs entries", strings.Join(names, " "))

	v, ok := os.LookupEnv("TEST_SECRET")
	check("TEST_SECRET present in env", ok, fmt.Sprintf("len=%d", len(v)))
	fmt.Printf("%-44s uid=%d gid=%d\n", "identity", os.Getuid(), os.Getgid())
	capEff := "unknown"
	if f, err := os.Open("/proc/self/status"); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "CapEff:") || strings.HasPrefix(sc.Text(), "NoNewPrivs:") {
				capEff = strings.Join(strings.Fields(sc.Text()), "=")
				fmt.Printf("%-44s %s\n", "proc status", capEff)
			}
		}
		f.Close()
	}
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) > 90 {
		s = s[:90] + "..."
	}
	return s
}
