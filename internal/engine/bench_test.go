package engine

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// BenchmarkEngine measures whole-engine cost per request against the
// in-process test server (b.N requests). Run with:
//
//	go test -run xxx -bench Engine -benchmem ./internal/engine/
func BenchmarkEngine(b *testing.B) {
	srv := startTestServer(b)
	for _, client := range clients {
		for _, vus := range []int{1, 32} {
			for _, sc := range []struct{ name, step string }{
				{"ping", `{id: a, request: {url: BASE/ping}}`},
				{"extract", `{id: a, request: {method: POST, url: BASE/echo, headers: {X-Id: "{{$uuid}}"}, body: {type: json, content: '{"token":"t-{{$randomInt(1,999)}}"}'}}, extract: [{name: tok, from: jsonpath, expr: $.token}], assert: [{type: status, equals: 200}]}`},
			} {
				b.Run(fmt.Sprintf("%s/%s/vus=%d", client, sc.name, vus), func(b *testing.B) {
					src := fmt.Sprintf(`
steps: [%s]
load: {max_requests: %d, stages: [{duration: 0s, target: %d}, {duration: 1h, target: %d}]}
`, sc.step, b.N, vus, vus)
					p := compile(b, src, srv.URL(), nil)
					e, err := New(p, Options{Client: client})
					if err != nil {
						b.Fatal(err)
					}
					b.ReportAllocs()
					b.ResetTimer()
					go func() {
						for range e.Snapshots() {
						}
					}()
					start := time.Now()
					if err := e.Run(context.Background()); err != nil {
						b.Fatal(err)
					}
					b.StopTimer()
					s := e.Summary()
					if s.Total.Requests != int64(b.N) || s.Total.ErrorCount() != 0 {
						b.Fatalf("requests=%d errors=%v %+v", s.Total.Requests, s.Total.Errors, s.Samples)
					}
					b.ReportMetric(float64(b.N)/time.Since(start).Seconds(), "req/s")
				})
			}
		}
	}
}

func BenchmarkNanotime(b *testing.B) {
	for b.Loop() {
		nanotime()
	}
}

func BenchmarkAppendRequest(b *testing.B) {
	p := compile(b, `
steps: [{id: a, request: {url: "http://127.0.0.1:18080/ping", headers: {X-A: "1", Authorization: "Bearer abc"}}}]
load: {stages: [{duration: 1s, target: 1}]}`, "", nil)
	e, _ := New(p, Options{})
	v := newVU(e, 0)
	v.buildRequest(e.steps[0])
	buf := make([]byte, 0, 512)
	b.ReportAllocs()
	for b.Loop() {
		v.buildRequest(e.steps[0])
		buf = appendRequest(buf[:0], &v.req, false)
	}
}
