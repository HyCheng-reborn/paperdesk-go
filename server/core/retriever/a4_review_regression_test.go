package retriever

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Reconstructed from the independent review's stated examples, not its source.
func TestA4Review_StatusAndPartial(t *testing.T) {
	for _, tc := range []struct {
		name     string
		opt      a4cRealFakeOpt
		complete bool
		requests int
	}{
		{"http500_body400", a4cRealFakeOpt{QAStatus: 500, QABody: a4cESErrorBody}, false, 4},
		{"http401_body400", a4cRealFakeOpt{QAStatus: 401, QABody: a4cESErrorBody}, false, 4},
		{"http403_body400", a4cRealFakeOpt{QAStatus: 403, QABody: a4cESErrorBody}, false, 4},
		{"http429_body400", a4cRealFakeOpt{QAStatus: 429, QABody: a4cESErrorBody}, false, 4},
		{"qa_timeout", a4cRealFakeOpt{QAStatus: 200, QABody: strings.Replace(a4cSearchEmptyBody, `"timed_out":false`, `"timed_out":true`, 1)}, false, 4},
		{"qa_failed_shard", a4cRealFakeOpt{QAStatus: 200, QABody: strings.Replace(a4cSearchEmptyBody, `"failed":0`, `"failed":1`, 1)}, false, 4},
		{"control_timeout", a4cRealFakeOpt{ContentBody: strings.Replace(a4cSearchHitBody, `"timed_out":false`, `"timed_out":true`, 1)}, false, 3},
		{"control_failed_shard", a4cRealFakeOpt{ContentBody: strings.Replace(a4cSearchHitBody, `"failed":0`, `"failed":1`, 1)}, false, 3},
		{"missing_metadata", a4cRealFakeOpt{QAStatus: 200, QABody: `{"hits":{"total":{"value":0,"relation":"eq"},"hits":[]}}`}, false, 4},
		{"oversized_control", a4cRealFakeOpt{ContentBody: `{"padding":"` + strings.Repeat("x", (1<<20)+1) + `"}`}, false, 3},
		{"qa_empty", a4cRealFakeOpt{QAStatus: 200, QABody: a4cSearchEmptyBody}, true, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := a4cNewRealFake(t, tc.opt)
			v := a4cRunRealProbe(t, context.Background(), a4cOfflineTarget(f.url()), &a4Obs{}, 4)
			if v.Observed != tc.complete || v.Incomplete == tc.complete || v.Requests != tc.requests {
				t.Fatalf("unexpected verdict: %s", v.Summary())
			}
		})
	}
}

func TestA4Review_OversizedBodyClosed(t *testing.T) {
	f := a4cNewRealFake(t, a4cRealFakeOpt{ContentBody: `{"padding":"` + strings.Repeat("x", (1<<20)+1) + `"}`})
	c := a4cBuild(t, f.url(), a4cIndexName, a4cDims, "", "", true, &a4Obs{}, 4)
	p := a4cRunSearch(context.Background(), c, c.Content, "a4-kb")
	opened, closed := c.bodies.snapshot()
	if p.Complete() || opened != 1 || closed != 1 {
		t.Fatalf("p=%s open=%d close=%d", p.Summary(), opened, closed)
	}
}

func TestA4Review_ConcurrentResponseAssociation(t *testing.T) {
	f := a4cNewRealFake(t, a4cRealFakeOpt{QAStatus: 500, QABody: a4cESErrorBody})
	c := a4cBuild(t, f.url(), a4cIndexName, a4cDims, "", "", true, &a4Obs{}, 2)
	ch := make(chan a4cProbe, 2)
	go func() { ch <- a4cRunSearch(context.Background(), c, c.Content, "a4-kb") }()
	go func() { ch <- a4cRunSearch(context.Background(), c, c.QA, "a4-kb") }()
	a, b := <-ch, <-ch
	if !((a.Kind == a4cProbeOK && b.Status == 500 && !b.Complete()) || (b.Kind == a4cProbeOK && a.Status == 500 && !a.Complete())) {
		t.Fatalf("unexpected responses %s / %s", a.Summary(), b.Summary())
	}
}

func TestA4Review_RedactionAndDimensions(t *testing.T) {
	if !strings.Contains((a4cVerdict{}).Summary(), "query_vec=not_generated") {
		t.Error("summary must not claim an ungenerated vector")
	}
	with := func(k, v string) func(string) string {
		m := map[string]string{a4cAllowEnv: "1", a4cAuthzEnv: a4cAuthzPhrase, a4cURLEnv: "http://127.0.0.1:9200", a4cIndexEnv: a4cIndexName, a4cIndexUUIDEnv: a4cFakeUUID, a4cKBEnv: "a4-kb"}
		m[k] = v
		return func(s string) string { return m[s] }
	}
	_, _, err := a4cResolveRealTarget(with(a4cURLEnv, "http://user:SECRET_MARKER@127.0.0.1:invalid"))
	if err == nil || strings.Contains(err.Error(), "SECRET_MARKER") {
		t.Errorf("unsafe URL error")
	}
	for _, tc := range []struct {
		raw  string
		want int
	}{{"", 1024}, {"512", 512}, {"4096", 4096}} {
		got, enabled, err := a4cResolveRealTarget(with(a4cDimsEnv, tc.raw))
		if err != nil || !enabled || got.Dims != tc.want {
			t.Errorf("dimensions %q: enabled=%v got=%d err=%v", tc.raw, enabled, got.Dims, err)
		}
	}
	for _, v := range []string{"1024junk", "0", "-1", "9999999999999999999999999999", "99999999"} {
		if _, _, err := a4cResolveRealTarget(with(a4cDimsEnv, v)); err == nil {
			t.Errorf("accepted invalid dimensions %q", v)
		}
	}
	if p := (a4cProbe{Kind: a4cProbeUndeclared4xx, Status: http.StatusBadRequest, ErrType: "PRIVATE_MARKER\nINJECTED", RootCause: "PRIVATE_MARKER"}).Summary(); strings.Contains(p, "PRIVATE_MARKER") || strings.Contains(p, "\n") {
		t.Errorf("unsafe type summary")
	}
	badBody := `{"error":{"root_cause":[{"type":"PRIVATE_MARKER\nINJECTED"}],"type":"PRIVATE_MARKER\nINJECTED"},"status":400}`
	secretFake := a4cNewRealFake(t, a4cRealFakeOpt{QABody: badBody})
	secretVerdict := a4cRunRealProbe(t, context.Background(), a4cOfflineTarget(secretFake.url()), &a4Obs{}, 4)
	if s := secretVerdict.Summary(); !secretVerdict.Incomplete || strings.Contains(s, "PRIVATE_MARKER") || strings.Contains(s, "\n") {
		t.Errorf("unsafe SDK verdict")
	}
	f := a4cNewRealFake(t, a4cRealFakeOpt{MappingBody: a4cMappingJSON(a4cIndexName, map[string]int{a4cContentFld: 512})})
	tgt := a4cOfflineTarget(f.url())
	tgt.Dims = 512
	v := a4cRunRealProbe(t, context.Background(), tgt, &a4Obs{}, 4)
	if !strings.Contains(v.Summary(), fmt.Sprintf("query_vec=e0/%d", 512)) {
		t.Errorf("dimension summary: %s", v.Summary())
	}
}
