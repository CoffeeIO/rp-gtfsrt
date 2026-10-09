package metrics_test

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/CoffeeIO/rp-gtfsrt/internal/metrics"
)

func TestExposition(t *testing.T) {
	r := metrics.New()
	r.Inc("calls_total", "API calls.", "kind", "board", "status", "ok")
	r.Inc("calls_total", "API calls.", "status", "ok", "kind", "board") // label order irrelevant
	r.Add("calls_total", "API calls.", 0.5, "kind", "journey", "status", "ok")
	r.Set("entities", "Feed entities.", 7)
	r.Set("entities", "Feed entities.", 9)
	r.Set("quoted", "Escaping.", 1, "v", `a"b\c`+"\n")

	var sb strings.Builder
	if err := r.Write(&sb); err != nil {
		t.Fatal(err)
	}
	want := `# HELP calls_total API calls.
# TYPE calls_total counter
calls_total{kind="board",status="ok"} 2
calls_total{kind="journey",status="ok"} 0.5
# HELP entities Feed entities.
# TYPE entities gauge
entities 9
# HELP quoted Escaping.
# TYPE quoted gauge
quoted{v="a\"b\\c\n"} 1
`
	if sb.String() != want {
		t.Fatalf("got:\n%s\nwant:\n%s", sb.String(), want)
	}
}

func TestHandler(t *testing.T) {
	r := metrics.New()
	r.Set("up", "Up.", 1)
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body, _ := io.ReadAll(rec.Body)
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") || !strings.Contains(string(body), "up 1") {
		t.Fatalf("response = %s %s", rec.Header(), body)
	}
}

func TestOddLabelsPanic(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	metrics.New().Inc("x", "x", "lonely")
}
