package speedtest

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func server(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/meta", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"clientIp":"203.0.113.9","asOrganization":"Example ISP","colo":{"iata":"LAX","city":"Los Angeles"}}`)
	})
	mux.HandleFunc("/__down", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("bytes"))
		time.Sleep(2 * time.Millisecond)
		buf := make([]byte, 32<<10)
		for n > 0 {
			k := min(n, len(buf))
			if _, err := w.Write(buf[:k]); err != nil {
				return
			}
			n -= k
		}
	})
	mux.HandleFunc("/__up", func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func quick(base string) *Test {
	t := New()
	t.Base = base
	t.Pings = 5
	t.Duration = 600 * time.Millisecond
	t.Warmup = 100 * time.Millisecond
	t.Streams = 2
	t.ChunkDown = 2_000_000
	t.ChunkUp = 1_000_000
	t.ReportEvery = 50 * time.Millisecond
	return t
}

func TestRun(t *testing.T) {
	s := server(t)
	var phases []Phase
	var lastFraction float64
	res, err := quick(s.URL).Run(context.Background(), func(p Progress) {
		if len(phases) == 0 || phases[len(phases)-1] != p.Phase {
			phases = append(phases, p.Phase)
		}
		if p.Fraction+1e-9 < lastFraction {
			t.Errorf("progress went backwards: %v after %v", p.Fraction, lastFraction)
		}
		lastFraction = p.Fraction
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.PingMs <= 0 || res.JitterMs < 0 || res.DownMbps <= 0 || res.UpMbps <= 0 {
		t.Fatalf("bad result %+v", res)
	}
	if res.Server != "LAX (Los Angeles)" || res.IP != "203.0.113.9" || res.ISP != "Example ISP" {
		t.Fatalf("bad meta %+v", res)
	}
	want := []Phase{PhaseLatency, PhaseDownload, PhaseUpload, PhaseDone}
	if len(phases) != len(want) {
		t.Fatalf("phases %v", phases)
	}
	for i := range want {
		if phases[i] != want[i] {
			t.Fatalf("phases %v", phases)
		}
	}
	t.Logf("%+v", res)
}

func TestCancel(t *testing.T) {
	s := server(t)
	ctx, cancel := context.WithCancel(context.Background())
	tt := quick(s.URL)
	tt.Duration = 5 * time.Second
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()
	start := time.Now()
	if _, err := tt.Run(ctx, nil); err == nil {
		t.Fatal("expected an error after cancel")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("cancel took too long")
	}
}

func TestServerDown(t *testing.T) {
	s := server(t)
	s.Close()
	if _, err := quick(s.URL).Run(context.Background(), nil); err == nil {
		t.Fatal("expected an error")
	}
}
