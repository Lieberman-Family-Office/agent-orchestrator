package linkpreview

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDefaultClientUsesPublicTransport(t *testing.T) {
	// Proxy-side DNS would bypass destination validation, even on a public proxy.
	t.Setenv("HTTP_PROXY", "http://proxy.test:8080")
	t.Setenv("HTTPS_PROXY", "http://proxy.test:8080")
	client := New(nil).client
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T, want a dedicated *http.Transport", client.Transport)
	}
	t.Cleanup(transport.CloseIdleConnections)
	if transport.Proxy != nil || transport.DialContext == nil || transport.DialTLSContext != nil || transport.DialTLS != nil { //nolint:staticcheck // Verify the deprecated TLS hook stays disabled; it bypasses destination validation.
		t.Fatal("transport permits a proxy or a dial path outside destination validation")
	}
	if transport.TLSClientConfig != nil {
		t.Fatal("transport must retain standard TLS hostname and certificate verification")
	}
	// Verify the default wiring, with cancellation preventing real I/O even if
	// this regresses to the standard, unrestricted dialer.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := transport.DialContext(ctx, "tcp", "127.0.0.1:80"); err == nil || !strings.Contains(err.Error(), "destination is not public") {
		t.Fatalf("default dialer err = %v, want destination rejection", err)
	}
	if !transport.ForceAttemptHTTP2 || transport.MaxIdleConns != 100 || transport.IdleConnTimeout != 90*time.Second || transport.TLSHandshakeTimeout != 10*time.Second || transport.ExpectContinueTimeout != time.Second {
		t.Fatal("standard transport limits or HTTP/2 support were lost")
	}
	if client.Timeout != fetchTimeout || client.CheckRedirect == nil {
		t.Fatal("fetch timeout or redirect limit was lost")
	}
	if err := client.CheckRedirect(nil, make([]*http.Request, maxRedirects)); err == nil {
		t.Fatal("redirect limit was not enforced")
	}
}

func TestPublicDialerRejectsNonPublicAddresses(t *testing.T) {
	for _, address := range []string{
		"0.0.0.0", "0.1.2.3", "10.1.2.3", "100.64.0.1", "127.0.0.1",
		"169.254.169.254", "172.16.0.1", "192.168.1.1", "192.0.0.1",
		"192.0.2.1", "192.88.99.1", "198.18.0.1", "198.51.100.1", "203.0.113.1",
		"224.0.0.1", "240.0.0.1", "255.255.255.255", "::", "::1",
		"::ffff:127.0.0.1", "::ffff:10.0.0.1", "::127.0.0.1", "fc00::1",
		"fe80::1", "fe80::1%en0", "fec0::1", "ff02::1", "100::1",
		"64:ff9b::7f00:1", "64:ff9b:1::a00:1", "2001::1", "2001:2::1",
		"2001:10::1", "2001:20::1", "2001:db8::1", "2002:7f00:1::1",
		"3ffe::1", "3fff::1", "4000::1", "2606:4700:4700::1111%en0",
	} {
		t.Run(address, func(t *testing.T) {
			ip := netip.MustParseAddr(address)
			d := publicDialer{
				lookup: func(context.Context, string, string) ([]netip.Addr, error) {
					return []netip.Addr{ip}, nil
				},
				dial: func(context.Context, string, string) (net.Conn, error) {
					t.Error("dial reached a non-public destination")
					return nil, errors.New("unexpected dial")
				},
			}
			// Exercise both DNS answers and literal URLs at the connection boundary.
			for _, host := range []string{"preview.test", address} {
				if _, err := d.dialContext(context.Background(), "tcp", net.JoinHostPort(host, "443")); err == nil {
					t.Errorf("destination %q was accepted", host)
				}
			}
		})
	}
}

func TestPublicDialerValidatesEveryDNSAnswerBeforeDialing(t *testing.T) {
	public := netip.MustParseAddr("8.8.8.8")
	private := netip.MustParseAddr("10.0.0.1")
	for name, answers := range map[string][]netip.Addr{
		"public first":   {public, private},
		"private first":  {private, public},
		"mixed families": {public, netip.MustParseAddr("::1")},
		"invalid answer": {public, {}},
		"no answers":     nil,
	} {
		t.Run(name, func(t *testing.T) {
			d := publicDialer{
				lookup: func(_ context.Context, network, host string) ([]netip.Addr, error) {
					if network != "ip" || host != "preview.test" {
						t.Errorf("lookup(%q, %q), want both address families", network, host)
					}
					return answers, nil
				},
				dial: func(context.Context, string, string) (net.Conn, error) {
					t.Error("dial ran before all answers passed validation")
					return nil, errors.New("unexpected dial")
				},
			}
			if _, err := d.dialContext(context.Background(), "tcp", "preview.test:80"); err == nil {
				t.Fatal("unsafe DNS answers were accepted")
			}
		})
	}
}

func TestPublicDialerPinsAddressesAndTriesPublicFallbacks(t *testing.T) {
	lookups := 0
	var dialed []string
	failed := errors.New("fake connection failure")
	d := publicDialer{
		lookup: func(ctx context.Context, _, _ string) ([]netip.Addr, error) {
			if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > fetchTimeout {
				t.Error("DNS lookup has no bounded deadline")
			}
			lookups++
			return []netip.Addr{
				netip.MustParseAddr("8.8.8.8"),
				netip.MustParseAddr("2606:4700:4700::1111"),
				netip.MustParseAddr("::ffff:1.1.1.1"),
			}, nil
		},
		dial: func(_ context.Context, network, address string) (net.Conn, error) {
			if network != "tcp" {
				t.Errorf("network = %q, want tcp", network)
			}
			dialed = append(dialed, address)
			return nil, failed
		},
	}
	_, err := d.dialContext(context.Background(), "tcp", "preview.test:8443")
	if !errors.Is(err, failed) || lookups != 1 {
		t.Fatalf("err = %v, lookups = %d; want one lookup and the dial error", err, lookups)
	}
	if got := strings.Join(dialed, ","); got != "8.8.8.8:8443,[2606:4700:4700::1111]:8443,1.1.1.1:8443" {
		t.Fatalf("dialed %q, want only validated numeric addresses with the original port", got)
	}
}

// previewResponseConn exercises the real HTTP transport without opening a socket.
func previewResponseConn(t *testing.T, response string) net.Conn {
	t.Helper()
	client, server := net.Pipe()
	done := make(chan struct{})
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
		<-done
	})
	go func() {
		defer close(done)
		defer func() { _ = server.Close() }()
		req, err := http.ReadRequest(bufio.NewReader(server))
		if err != nil {
			t.Errorf("read fake upstream request: %v", err)
			return
		}
		_ = req.Body.Close()
		if _, err := io.WriteString(server, response); err != nil {
			t.Errorf("write fake upstream response: %v", err)
		}
	}()
	return client
}

func TestPreviewValidatesRedirectConnections(t *testing.T) {
	for _, tc := range []struct {
		name        string
		target      string
		answers     []netip.Addr
		wantSuccess bool
	}{
		{"public", "http://next.test/page", []netip.Addr{netip.MustParseAddr("1.1.1.1")}, true},
		{"public IPv6", "http://[2606:4700:4700::1111]/page", nil, true},
		{"loopback", "http://127.0.0.1/page", nil, false},
		{"HTTPS loopback", "https://[::1]/page", nil, false},
		{"private DNS", "http://next.test/page", []netip.Addr{netip.MustParseAddr("10.0.0.1")}, false},
		{"mixed DNS", "http://next.test/page", []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("::1")}, false},
		{"same host rebinding", "http://start.test/page", []netip.Addr{netip.MustParseAddr("127.0.0.1")}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := New(nil)
			transport, ok := svc.client.Transport.(*http.Transport)
			if !ok {
				t.Fatalf("transport = %T", svc.client.Transport)
			}
			t.Cleanup(transport.CloseIdleConnections)
			var lookups, dials atomic.Int32
			d := publicDialer{
				lookup: func(context.Context, string, string) ([]netip.Addr, error) {
					if lookups.Add(1) == 1 {
						return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
					}
					return tc.answers, nil
				},
				dial: func(_ context.Context, _, address string) (net.Conn, error) {
					if dials.Add(1) == 1 {
						if address != "8.8.8.8:80" {
							t.Errorf("initial dial = %q", address)
						}
						return previewResponseConn(t, "HTTP/1.1 302 Found\r\nLocation: "+tc.target+"\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"), nil
					}
					if !tc.wantSuccess || (address != "1.1.1.1:80" && address != "[2606:4700:4700::1111]:80") {
						t.Errorf("unsafe or unresolved redirect dial = %q", address)
						return nil, errors.New("unexpected redirect dial")
					}
					body := "<html><head><title>Public redirect</title></head></html>"
					return previewResponseConn(t, fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Type: text/html\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)), nil
				},
			}
			transport.DialContext = d.dialContext
			p, err := svc.Preview(context.Background(), "http://start.test/start")
			if tc.wantSuccess {
				if err != nil || p.URL != tc.target || p.Title != "Public redirect" || dials.Load() != 2 {
					t.Fatalf("preview = %+v, err = %v, dials = %d", p, err, dials.Load())
				}
			} else if !errors.Is(err, ErrFetchFailed) || dials.Load() != 1 {
				t.Fatalf("err = %v, dials = %d; want fetch failure before redirect dial", err, dials.Load())
			}
		})
	}
}

func TestParseHeadSkipsScriptBodies(t *testing.T) {
	doc := []byte(`<html><head>
<script>
const fake = '<meta property="og:title" content="Script Title">';
document.write('<meta property="og:description" content="Script Description">');
</script>
<meta property="og:title" content="Real Title">
<meta property="og:description" content="Real Description">
</head><body></body></html>`)

	m := parseHead(doc)
	if m.ogTitle != "Real Title" {
		t.Errorf("ogTitle = %q, want the tag after the script body to win", m.ogTitle)
	}
	if m.ogDescription != "Real Description" {
		t.Errorf("ogDescription = %q, want the tag after the script body to win", m.ogDescription)
	}
}

func TestParseHeadKeepsColorSchemeIconsApart(t *testing.T) {
	doc := []byte(`<html><head>
<link rel="icon" href="/dark.svg" media="(prefers-color-scheme: dark)">
<link rel="icon" href="/light.svg" media="(prefers-color-scheme:light)">
<link rel="icon" href="/plain.png">
</head><body></body></html>`)

	m := parseHead(doc)
	if m.favicon != "/plain.png" || m.faviconLight != "/light.svg" || m.faviconDark != "/dark.svg" {
		t.Errorf("icons = %q / light %q / dark %q, want the dark-only icon kept out of the default", m.favicon, m.faviconLight, m.faviconDark)
	}
}

func TestParseHeadSkipsStyleBodies(t *testing.T) {
	doc := []byte(`<html><head>
<style>
.x::before { content: '<meta property="og:title" content="Style Title">'; }
</style>
<meta property="og:title" content="Real Title">
</head><body></body></html>`)

	m := parseHead(doc)
	if m.ogTitle != "Real Title" {
		t.Errorf("ogTitle = %q, want the tag after the style body to win", m.ogTitle)
	}
}

func TestParseHeadUnterminatedCommentDoesNotSwallowRest(t *testing.T) {
	doc := []byte(`<html><head>
<!-- a comment that never closes
<meta property="og:title" content="Still Found">
<meta name="description" content="Also Found">
</head><body></body></html>`)

	m := parseHead(doc)
	if m.ogTitle != "Still Found" {
		t.Errorf("ogTitle = %q, want tags after an unterminated comment to be extracted", m.ogTitle)
	}
	if m.description != "Also Found" {
		t.Errorf("description = %q, want tags after an unterminated comment to be extracted", m.description)
	}
}

func TestParseHeadUnterminatedScriptSkipsToEnd(t *testing.T) {
	doc := []byte(`<html><head>
<meta property="og:title" content="Before Script">
<script>const s = '<meta property="og:description" content="Inside Script">';`)

	m := parseHead(doc)
	if m.ogTitle != "Before Script" {
		t.Errorf("ogTitle = %q", m.ogTitle)
	}
	if m.ogDescription != "" {
		t.Errorf("ogDescription = %q, want script content ignored even without a close tag", m.ogDescription)
	}
}

func TestPreviewConcurrentSameURLFetchesOnce(t *testing.T) {
	hits := &atomic.Int32{}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		// Slow enough that every concurrent caller overlaps the first fetch.
		time.Sleep(50 * time.Millisecond)
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<html><head><title>T</title></head><body></body></html>`)
	}))
	defer origin.Close()

	svc := New(origin.Client())
	const callers = 8
	var wg sync.WaitGroup
	errs := make([]error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = svc.Preview(context.Background(), origin.URL+"/page")
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("origin hits = %d, want 1 (singleflight dedup)", got)
	}
}

// A tag byte that is neither '>' nor '/' nor a name character (a stray quote,
// '=' or '<') must not stall the parser: the daemon parses hovered pages on
// request goroutines with no deadline, so a non-advancing loop spins a core
// forever and survives the request. Observed 2026-10-04: three hovers, three
// cores, until the daemon was restarted.
func TestParseHeadAdvancesPastStrayTagBytes(t *testing.T) {
	cases := map[string]string{
		"stray quote after attribute": `<html><head><meta name="description" content="x""><title>T</title></head><body></body></html>`,
		"lone equals sign":            `<html><head><link rel="icon" = href="/f.ico"><title>T</title></head></html>`,
		"angle bracket inside tag":    `<html><head><title <>T</title></head></html>`,
		"stray quote at tag start":    `<html><head><meta "><title>T</title></head></html>`,
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			done := make(chan headMeta, 1)
			go func() { done <- parseHead([]byte(doc)) }()
			select {
			case m := <-done:
				if m.title != "T" {
					t.Fatalf("title = %q, want %q", m.title, "T")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("parseHead did not return within 2s: parser loop does not advance")
			}
		})
	}
}

// htmlOrigin serves a titled page to /page and counts hits.
func htmlOrigin(t *testing.T, wait <-chan struct{}) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	hits := &atomic.Int32{}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		if wait != nil {
			<-wait
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<html><head><title>Real</title></head><body></body></html>`)
	}))
	t.Cleanup(origin.Close)
	return origin, hits
}

func waitForHits(t *testing.T, hits *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for hits.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("origin hits = %d, want %d", hits.Load(), want)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// Leaving a link must cost only that hover: the page was healthy, so the next
// hover has to get the page, not a cached failure.
func TestPreviewAbandonedHoverDoesNotPoisonCache(t *testing.T) {
	release := make(chan struct{})
	origin, hits := htmlOrigin(t, release)
	svc := New(origin.Client())

	ctx, cancel := context.WithCancel(context.Background())
	abandoned := make(chan error, 1)
	go func() {
		_, err := svc.Preview(ctx, origin.URL+"/page")
		abandoned <- err
	}()
	waitForHits(t, hits, 1)
	cancel()
	select {
	case err := <-abandoned:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("abandoned caller err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("abandoned caller kept waiting for the upstream")
	}

	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for {
		p, err := svc.Preview(context.Background(), origin.URL+"/page")
		if err == nil {
			if p.Title != "Real" {
				t.Fatalf("title = %q", p.Title)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("next hover never recovered: %v", err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("origin hits = %d, want 1 (the abandoned fetch fills the cache)", got)
	}
}

// A second hover that joined the fetch must not fail because the first left.
func TestPreviewJoinedCallerSurvivesFirstCallerLeaving(t *testing.T) {
	release := make(chan struct{})
	origin, hits := htmlOrigin(t, release)
	svc := New(origin.Client())

	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() {
		_, err := svc.Preview(ctx, origin.URL+"/page")
		first <- err
	}()
	waitForHits(t, hits, 1)

	joined := make(chan error, 1)
	go func() {
		_, err := svc.Preview(context.Background(), origin.URL+"/page")
		joined <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	<-first
	close(release)

	select {
	case err := <-joined:
		if err != nil {
			t.Fatalf("joined caller err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("joined caller never returned")
	}
}

func TestPreviewStopsReadingAtHeadClose(t *testing.T) {
	bodyDone := make(chan struct{})
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<html><head><title>Fast head</title></head><body>`)
		w.(http.Flusher).Flush()
		<-bodyDone
	}))
	t.Cleanup(origin.Close)
	// Cleanups run last-first: release the handler before Close waits for it.
	t.Cleanup(func() { close(bodyDone) })

	svc := New(origin.Client())
	svc.fetchTimeout = 5 * time.Second
	started := time.Now()
	p, err := svc.Preview(context.Background(), origin.URL+"/page")
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if p.Title != "Fast head" {
		t.Fatalf("title = %q", p.Title)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("Preview waited %s for a body it does not need", elapsed)
	}
}

// oneByteReader forces the head marker to straddle reads.
type oneByteReader struct{ r io.Reader }

func (o oneByteReader) Read(p []byte) (int, error) { return o.r.Read(p[:1]) }

func TestReadHeadFindsMarkerAcrossReads(t *testing.T) {
	doc := "<head><title>T</title></HEAD>" + strings.Repeat("x", 4096)
	got, err := readHead(oneByteReader{strings.NewReader(doc)})
	if err != nil {
		t.Fatalf("readHead: %v", err)
	}
	if want := len("<head><title>T</title></HEAD"); len(got) != want {
		t.Fatalf("read %d bytes, want it to stop at the close tag (%d)", len(got), want)
	}
}

func TestPreviewHungUpstreamIsBounded(t *testing.T) {
	release := make(chan struct{})
	origin, _ := htmlOrigin(t, release)
	t.Cleanup(func() { close(release) })

	svc := New(origin.Client())
	svc.fetchTimeout = 50 * time.Millisecond
	done := make(chan error, 1)
	go func() {
		_, err := svc.Preview(context.Background(), origin.URL+"/page")
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrFetchFailed) {
			t.Fatalf("err = %v, want ErrFetchFailed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hung upstream was not bounded by the fetch timeout")
	}
}

type panicTransport struct{}

func (panicTransport) RoundTrip(*http.Request) (*http.Response, error) { panic("fetch bug") }

// The shared fetch runs where no HTTP recover reaches it, so without its own
// recover this panic would end the test binary, as it would the daemon.
func TestPreviewPanicBecomesFetchFailure(t *testing.T) {
	svc := New(&http.Client{Transport: panicTransport{}})
	if _, err := svc.Preview(context.Background(), "http://example.test/page"); !errors.Is(err, ErrFetchFailed) {
		t.Fatalf("err = %v, want ErrFetchFailed", err)
	}
}

func TestPreviewFragmentsShareOneFetch(t *testing.T) {
	origin, hits := htmlOrigin(t, nil)
	svc := New(origin.Client())
	for _, fragment := range []string{"#issuecomment-1", "#issuecomment-2", ""} {
		if _, err := svc.Preview(context.Background(), origin.URL+"/page"+fragment); err != nil {
			t.Fatalf("Preview(%q): %v", fragment, err)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("origin hits = %d, want 1", got)
	}
}

func TestPreviewFailureCacheWindows(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusInternalServerError)
	hits := &atomic.Int32{}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/binary" {
			w.Header().Set("Content-Type", "application/pdf")
			return
		}
		if code := int(status.Load()); code != http.StatusOK {
			w.WriteHeader(code)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<html><head><title>Back</title></head></html>`)
	}))
	t.Cleanup(origin.Close)

	now := time.Unix(1_000_000, 0)
	svc := New(origin.Client())
	svc.now = func() time.Time { return now }

	if _, err := svc.Preview(context.Background(), origin.URL+"/page"); !errors.Is(err, ErrFetchFailed) {
		t.Fatalf("err = %v, want ErrFetchFailed", err)
	}
	status.Store(http.StatusOK)

	now = now.Add(fetchFailureTTL - time.Second)
	if _, err := svc.Preview(context.Background(), origin.URL+"/page"); !errors.Is(err, ErrFetchFailed) {
		t.Fatalf("within window err = %v, want the cached failure", err)
	}
	now = now.Add(2 * time.Second)
	p, err := svc.Preview(context.Background(), origin.URL+"/page")
	if err != nil || p.Title != "Back" {
		t.Fatalf("after window got %+v, %v; want the recovered page", p, err)
	}

	if _, err := svc.Preview(context.Background(), origin.URL+"/binary"); err == nil {
		t.Fatal("binary URL should fail")
	}
	before := hits.Load()
	now = now.Add(fetchFailureTTL + time.Second)
	if _, err := svc.Preview(context.Background(), origin.URL+"/binary"); err == nil {
		t.Fatal("binary URL should fail")
	}
	if got := hits.Load(); got != before {
		t.Fatalf("non-HTML result should outlive the fetch failure window; hits %d -> %d", before, got)
	}
}
