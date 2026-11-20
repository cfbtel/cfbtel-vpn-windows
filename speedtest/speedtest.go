/* SPDX-License-Identifier: MIT
 *
 * Copyright (C) 2026 CFBTEL. All Rights Reserved.
 */

// Package speedtest measures ping, jitter, download and upload speed against Cloudflare's speed test network
// (speed.cloudflare.com), the same endpoints its own web speed test uses.
package speedtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

const DefaultBase = "https://speed.cloudflare.com"

// ErrBusy: the speed test server keeps refusing (it limits how often one address may test).
var ErrBusy = errors.New("Cloudflare's speed test is busy for your internet address right now. Wait a minute and try again.")

func (t *Test) newRequest(ctx context.Context, method, url string, body io.Reader) *http.Request {
	req, _ := http.NewRequestWithContext(ctx, method, url, body)
	req.Header.Set("Referer", DefaultBase+"/")
	return req
}

type Phase int

const (
	PhaseIdle Phase = iota
	PhaseLatency
	PhaseDownload
	PhaseUpload
	PhaseDone
)

// Progress is reported several times a second while a test runs.
type Progress struct {
	Phase    Phase
	Fraction float64 // 0..1 of the whole test
	Mbps     float64 // live speed of the current phase
	Result   Result  // what is known so far
}

type Result struct {
	PingMs   float64
	JitterMs float64
	DownMbps float64
	UpMbps   float64
	Server   string // Cloudflare location, e.g. "LAX (Los Angeles)"
	IP       string // the address the test came from
	ISP      string
	At       time.Time
}

// Test settings; the defaults give about 25 seconds in total.
type Test struct {
	Base        string
	Client      *http.Client
	Pings       int
	Duration    time.Duration // per direction
	Warmup      time.Duration // not counted at the start of each direction
	Streams     int
	ChunkDown   int64
	ChunkUp     int64
	ReportEvery time.Duration
}

func New() *Test {
	return &Test{
		Base:        DefaultBase,
		Client:      &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, MaxIdleConnsPerHost: 16, ForceAttemptHTTP2: false}},
		Pings:       20,
		Duration:    10 * time.Second,
		Warmup:      time.Second,
		Streams:     4,
		ChunkDown:   25_000_000, // largest piece; each stream starts at 1 MB and doubles
		ChunkUp:     4_000_000,
		ReportEvery: 200 * time.Millisecond,
	}
}

// Run does the whole test. progress may be nil. Cancel ctx to stop it.
func (t *Test) Run(ctx context.Context, progress func(Progress)) (Result, error) {
	report := func(p Progress) {
		if progress != nil {
			progress(p)
		}
	}
	res := Result{At: time.Now()}
	res.Server, res.IP, res.ISP = t.meta(ctx)
	report(Progress{Phase: PhaseLatency, Result: res})

	ping, jitter, err := t.latency(ctx, func(f float64) { report(Progress{Phase: PhaseLatency, Fraction: f * 0.1, Result: res}) })
	if err != nil {
		return res, err
	}
	res.PingMs, res.JitterMs = ping, jitter

	down, err := t.transfer(ctx, false, func(f, mbps float64) {
		report(Progress{Phase: PhaseDownload, Fraction: 0.1 + f*0.45, Mbps: mbps, Result: res})
	})
	if err != nil {
		return res, err
	}
	res.DownMbps = down

	up, err := t.transfer(ctx, true, func(f, mbps float64) {
		report(Progress{Phase: PhaseUpload, Fraction: 0.55 + f*0.45, Mbps: mbps, Result: res})
	})
	if err != nil {
		return res, err
	}
	res.UpMbps = up
	report(Progress{Phase: PhaseDone, Fraction: 1, Result: res})
	return res, nil
}

func (t *Test) meta(ctx context.Context) (server, ip, isp string) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.Base+"/meta", nil)
	if err != nil {
		return
	}
	resp, err := t.Client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var m struct {
		ClientIP       string `json:"clientIp"`
		AsOrganization string `json:"asOrganization"`
		Colo           any    `json:"colo"`
		City           string `json:"city"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&m) != nil {
		return
	}
	switch c := m.Colo.(type) {
	case string:
		server = c
	case map[string]any:
		if iata, ok := c["iata"].(string); ok {
			server = iata
		}
		if city, ok := c["city"].(string); ok && city != "" {
			server = fmt.Sprintf("%s (%s)", server, city)
		}
	}
	return server, m.ClientIP, m.AsOrganization
}

// latency times small requests on a warm connection: ping is the median, jitter the average change between
// consecutive pings.
func (t *Test) latency(ctx context.Context, progress func(float64)) (ping, jitter float64, err error) {
	var rtts []float64
	for i := 0; i <= t.Pings; i++ {
		if ctx.Err() != nil {
			return 0, 0, ctx.Err()
		}
		var start, first time.Time
		trace := &httptrace.ClientTrace{
			WroteRequest:         func(httptrace.WroteRequestInfo) { start = time.Now() },
			GotFirstResponseByte: func() { first = time.Now() },
		}
		rctx, cancel := context.WithTimeout(httptrace.WithClientTrace(ctx, trace), 5*time.Second)
		req := t.newRequest(rctx, http.MethodGet, t.Base+"/__down?bytes=0", nil)
		resp, err := t.Client.Do(req)
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusTooManyRequests {
				cancel()
				return 0, 0, ErrBusy
			}
			if resp.StatusCode != http.StatusOK {
				err = fmt.Errorf("the speed test server answered %s", resp.Status)
			}
		}
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return 0, 0, ctx.Err()
			}
			return 0, 0, fmt.Errorf("can't reach the speed test server: %w", err)
		}
		if i > 0 && !start.IsZero() && !first.IsZero() { // the first request opens the connection
			rtts = append(rtts, float64(first.Sub(start).Microseconds())/1000)
		}
		progress(float64(i) / float64(t.Pings))
	}
	if len(rtts) == 0 {
		return 0, 0, errors.New("no ping results")
	}
	for i := 1; i < len(rtts); i++ {
		d := rtts[i] - rtts[i-1]
		if d < 0 {
			d = -d
		}
		jitter += d
	}
	if len(rtts) > 1 {
		jitter /= float64(len(rtts) - 1)
	}
	sorted := append([]float64(nil), rtts...)
	sort.Float64s(sorted)
	n := len(sorted)
	ping = sorted[n/2]
	if n%2 == 0 {
		ping = (sorted[n/2-1] + sorted[n/2]) / 2
	}
	return ping, jitter, nil
}

type countingReader struct {
	left  int64
	count *atomic.Int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	if r.left <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > r.left {
		p = p[:r.left]
	}
	clear(p)
	r.left -= int64(len(p))
	r.count.Add(int64(len(p)))
	return len(p), nil
}

// transfer runs parallel downloads or uploads for t.Duration and returns the speed in Mbps, not counting the
// warm-up. progress gets the fraction done and the speed over the last second.
func (t *Test) transfer(ctx context.Context, upload bool, progress func(f, mbps float64)) (float64, error) {
	var count atomic.Int64
	var failed atomic.Pointer[error]
	tctx, cancel := context.WithTimeout(ctx, t.Warmup+t.Duration)
	defer cancel()

	var wg sync.WaitGroup
	var refused atomic.Int64
	for i := 0; i < t.Streams; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Stagger the streams and grow the pieces, like a browser test, so the server isn't hit all at once.
			select {
			case <-time.After(time.Duration(i) * 150 * time.Millisecond):
			case <-tctx.Done():
				return
			}
			buf := make([]byte, 64<<10)
			chunk := min(int64(1_000_000), t.ChunkDown)
			if upload {
				chunk = min(int64(1_000_000), t.ChunkUp)
			}
			for tctx.Err() == nil {
				var req *http.Request
				if upload {
					req = t.newRequest(tctx, http.MethodPost, t.Base+"/__up", &countingReader{left: chunk, count: &count})
					req.ContentLength = chunk
					req.Header.Set("Content-Type", "application/octet-stream")
				} else {
					req = t.newRequest(tctx, http.MethodGet, fmt.Sprintf("%s/__down?bytes=%d", t.Base, chunk), nil)
				}
				resp, err := t.Client.Do(req)
				if err != nil {
					if tctx.Err() == nil {
						failed.CompareAndSwap(nil, &err)
						cancel()
					}
					return
				}
				if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
					// The server wants a pause: wait and try again instead of failing the whole test.
					io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
					resp.Body.Close()
					refused.Add(1)
					select {
					case <-time.After(time.Second):
					case <-tctx.Done():
					}
					continue
				}
				if !upload {
					for {
						n, err := resp.Body.Read(buf)
						count.Add(int64(n))
						if err != nil {
							break
						}
					}
				} else {
					io.Copy(io.Discard, resp.Body)
				}
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK && tctx.Err() == nil {
					err := fmt.Errorf("the speed test server answered %s", resp.Status)
					failed.CompareAndSwap(nil, &err)
					cancel()
					return
				}
				limit := t.ChunkDown
				if upload {
					limit = t.ChunkUp
				}
				chunk = min(chunk*2, limit)
			}
		}(i)
	}

	start := time.Now()
	var counted int64 = -1
	var countedAt time.Time
	type sample struct {
		at time.Time
		n  int64
	}
	window := []sample{{start, 0}}
	ticker := time.NewTicker(t.ReportEvery)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
loop:
	for {
		select {
		case <-done:
			break loop
		case now := <-ticker.C:
			n := count.Load()
			if counted < 0 && now.Sub(start) >= t.Warmup {
				counted, countedAt = n, now
			}
			window = append(window, sample{now, n})
			for len(window) > 2 && now.Sub(window[0].at) > time.Second {
				window = window[1:]
			}
			live := 0.0
			if dt := now.Sub(window[0].at).Seconds(); dt > 0 {
				live = float64(n-window[0].n) * 8 / dt / 1e6
			}
			progress(min(1, float64(now.Sub(start))/float64(t.Warmup+t.Duration)), live)
		}
	}
	ticker.Stop()
	end := time.Now()
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	if e := failed.Load(); e != nil {
		return 0, fmt.Errorf("speed test interrupted: %w", *e)
	}
	if count.Load() == 0 && refused.Load() > 0 {
		return 0, ErrBusy
	}
	if counted < 0 {
		counted, countedAt = 0, start
	}
	secs := end.Sub(countedAt).Seconds()
	if secs <= 0 {
		return 0, errors.New("the test was too short")
	}
	return float64(count.Load()-counted) * 8 / secs / 1e6, nil
}
