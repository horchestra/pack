package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/horchestra/pack/internal/valuesfile"
	"github.com/horchestra/pack/pkg/api"
	"github.com/horchestra/pack/pkg/render"
)

// A release whose values lack api_addr, on a stand where 5433 is taken: what fill asks and what it is answered.
func fillRelease(t *testing.T) string {
	t.Helper()
	stand := filepath.Join(t.TempDir(), "stand.json")
	if err := os.WriteFile(stand, []byte(`{"hosts": {"db1": {"ports": [{"port": 5433, "proto": "tcp"}]}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	d := releaseOf(t, generated(t), map[string]any{"facts": stand})
	write(t, d, "values.yaml", map[string]any{"postgres": map[string]any{"id": "n1"}})
	pack(t, d, "discover")
	return d
}

func fillFlags(t *testing.T) {
	t.Helper()
	fillAccept, fillRewrite, answerFile, answerPatch, answerSource, jsonOut = false, false, "", "", valuesfile.FromOperator, false
	t.Cleanup(func() {
		fillAccept, fillRewrite, answerFile, answerPatch, answerSource, jsonOut = false, false, "", "", valuesfile.FromOperator, false
	})
}

func TestFill(t *testing.T) {
	d := fillRelease(t)
	fillFlags(t)
	out := pack(t, d, "fill", "--json")
	jsonOut = false
	var qs render.Questions
	if err := json.Unmarshal([]byte(out), &qs); err != nil {
		t.Fatalf("fill --json: %v\n%s", err, out)
	}
	byPtr := map[string]render.Question{}
	for _, q := range qs.Questions {
		byPtr[q.Pointer] = q
	}
	if q := byPtr["/openbao/api_addr"]; q.Reason != render.ReasonRequired {
		t.Errorf("api_addr: %+v", q)
	}
	if q := byPtr["/postgres/pg_port"]; q.Reason != render.ReasonSuggested || len(q.Candidates) == 0 || q.Candidates[0].Value != float64(5434) {
		t.Errorf("pg_port: %+v", q)
	}

	pack(t, d, "fill", "--accept-suggested")
	fillAccept = false
	text, _ := os.ReadFile(filepath.Join(d, "values.yaml"))
	if !strings.Contains(string(text), "# source: suggest — 5433 is taken on a host\n  pg_port: 5434\n") {
		t.Errorf("the suggestion taken:\n%s", text)
	}

	out = pack(t, d, "answer", "/openbao/api_addr", "https://bao:8200")
	if !strings.Contains(out, "/openbao/api_addr: — → https://bao:8200 (operator)") {
		t.Errorf("answer: %s", out)
	}
	pack(t, d, "answer", "/postgres/pg_password", "db/password")
	inRelease(t, d)
	if n := whatNext(context.Background()); n.State != StatePlanned {
		t.Errorf("answered, next: %+v", n)
	}
	text, _ = os.ReadFile(filepath.Join(d, "values.yaml"))
	if !strings.Contains(string(text), "secretRef:\n      key: password\n      name: db") {
		t.Errorf("a credential as a reference:\n%s", text)
	}

	out = pack(t, d, "values", "explain", "/postgres/pg_port")
	if !strings.Contains(out, "value: 5434 (suggest — 5433 is taken on a host)") || !strings.Contains(out, "default: 5433") {
		t.Errorf("explain:\n%s", out)
	}
}

func TestAnswerRefuses(t *testing.T) {
	d := fillRelease(t)
	fillFlags(t)
	inRelease(t, d)
	ctx := context.Background()
	rs, err := openAsk(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.s.Close()
	if _, err := rs.parse("/openbao/ui", "maybe"); err == nil {
		t.Error("not a boolean")
	}
	if _, err := rs.parse("/postgres/pg_password", "hunter2"); err == nil || !strings.Contains(err.Error(), "reference") {
		t.Errorf("a credential's value: %v", err)
	}
	if _, err := rs.parse("/postgres/pg_password", "db/pass/word"); err == nil {
		t.Error("a reference of three parts")
	}
	if _, err := rs.parse("/nope", "x"); err == nil {
		t.Error("a setting the pack has not")
	}
	before, _ := os.ReadFile(filepath.Join(d, "values.yaml"))
	_, err = rs.apply(ctx, []answer{{Pointer: "/openbao/log_level", Value: float64(7), Source: valuesfile.FromLLM}})
	if err == nil || classify(err).Code != api.CodeValuesInvalid || len(classify(err).Problems) == 0 {
		t.Errorf("a value the schema refuses: %v", err)
	}
	if after, _ := os.ReadFile(filepath.Join(d, "values.yaml")); string(after) != string(before) {
		t.Error("a refused answer was written")
	}

	patch := filepath.Join(t.TempDir(), "patch.json")
	_ = os.WriteFile(patch, []byte(`[{"op": "add", "path": "/openbao/api_addr", "value": "https://b:8200"}, {"op": "remove", "path": "/postgres/id"}]`), 0o644)
	answerSource = valuesfile.FromLLM
	as, err := rs.patchFile(patch)
	if err != nil || len(as) != 2 || !as[1].Remove {
		t.Fatalf("patch: %+v %v", as, err)
	}
	changes, err := rs.apply(ctx, as)
	if err != nil || len(changes) != 2 {
		t.Fatalf("patch applied: %+v %v", changes, err)
	}
	text, _ := os.ReadFile(filepath.Join(d, "values.yaml"))
	if !strings.Contains(string(text), "# source: llm\n  api_addr: https://b:8200") || strings.Contains(string(text), "\n  id: n1") {
		t.Errorf("the patch:\n%s", text)
	}
}

func TestInterview(t *testing.T) {
	qs := []render.Question{
		{Pointer: "/a", Candidates: []render.Candidate{{Value: "x", Source: render.FromSuggest, Why: "w"}, {Value: "y", Source: render.FromDefault}}},
		{Pointer: "/b", Candidates: []render.Candidate{{Value: "p"}}},
		{Pointer: "/c"},
		{Pointer: "/d", Candidates: []render.Candidate{{Value: "q"}, {Value: "r"}}},
	}
	parse := func(ptr, text string) (any, error) {
		if ptr == "/c" && text == "bad" {
			return nil, errors.New("not that")
		}
		return "parsed:" + text, nil
	}
	got, err := interview(qs, "", strings.NewReader("\n-\nbad\nfree text\n2\n"), parse)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Value != "x" || got[0].Source != valuesfile.FromSuggest || got[1].Pointer != "/c" || got[1].Value != "parsed:free text" || got[2].Value != "r" {
		t.Errorf("answers = %+v", got)
	}
	// =2 is the text 2; the end of the input answers nothing more
	got, _ = interview(qs, "", strings.NewReader("=2\n"), parse)
	if len(got) != 1 || got[0].Value != "parsed:2" {
		t.Errorf("=2 and the end: %+v", got)
	}
}
