package retriever

import (
	"context"
	"strings"
	"testing"
)

// Reconstructed from the independent review; not the reviewer's source.
func TestA4SecondReview_PrecheckOversize(t *testing.T) {
	meta := a4cMetaJSON(a4cIndexName, a4cFakeUUID, nil)
	mapping := a4cMappingJSON(a4cIndexName, map[string]int{a4cContentFld: a4cDims})
	pad := func(s string) string { return s + strings.Repeat(" ", a4cMaxRequestBody-len(s)) + `{}` }
	for _, tc := range []struct {
		name     string
		opt      a4cRealFakeOpt
		requests int
	}{
		{"index", a4cRealFakeOpt{MetaBody: pad(meta)}, 1},
		{"mapping", a4cRealFakeOpt{MappingBody: pad(mapping)}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := a4cNewRealFake(t, tc.opt)
			v := a4cRunRealProbe(t, context.Background(), a4cOfflineTarget(f.url()), &a4Obs{}, 4)
			if !v.Incomplete || v.Observed || v.Requests != tc.requests {
				t.Fatalf("unexpected verdict: %s", v.Summary())
			}
		})
	}
}

func TestA4SecondReview_MissingHits(t *testing.T) {
	for _, body := range []string{`{"timed_out":false,"_shards":{"failed":0}}`, `{"timed_out":false,"_shards":{"failed":0},"hits":{}}`, `{"timed_out":false,"_shards":{"failed":0},"hits":{"hits":null}}`} {
		f := a4cNewRealFake(t, a4cRealFakeOpt{QAStatus: 200, QABody: body})
		v := a4cRunRealProbe(t, context.Background(), a4cOfflineTarget(f.url()), &a4Obs{}, 4)
		if !v.Incomplete || v.Observed {
			t.Fatalf("unexpected verdict: %s", v.Summary())
		}
	}
}
