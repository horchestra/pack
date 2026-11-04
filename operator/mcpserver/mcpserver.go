// Package mcpserver is the operator's MCP server (К10): the tools a model runs a release with, over the
// operator's API — next, check, plan, status, history, logs, journal, request, job. What is a person's is
// not a tool's: no approval (the API has it, a person signs it), no secret's value.
package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Instructions are what the server tells the model of how to use it.
const Instructions = `Tools of the pack operator for the releases of its projects. A release is named
<project>:<path> (dev:releases/prod/db). Loop: next — it says the state (new, unrefreshed, unfilled, invalid,
failed, planned, applied) and what stops it; plan makes the plan and says its digest; request runs an action
(apply, rollback, delete, run, refresh, discover) as a job whose job tool reads the state and the events;
status, history, logs and journal read what is installed and what the sagas said. Approving a plan is a
person's: when a request waits for an approval, say so and stop — no tool approves. Secrets are never read:
a password is a reference, name and key.`

// Client is the operator's API as the tools reach it.
type Client struct {
	Base string       // the API's URL
	User string       // the X-Pack-User header, empty — the operator's own (auth: none)
	HTTP *http.Client // nil — http.DefaultClient
}

// result is a tool's answer: the API's document, or its error.
type result struct {
	OK     bool            `json:"ok"`
	Status int             `json:"status,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}

// get is one API call: the status and the body as they came.
func (c *Client) get(ctx context.Context, path string) (int, json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.Base+path, nil)
	if err != nil {
		return 0, nil, err
	}
	if len(c.User) > 0 {
		req.Header.Set("X-Pack-User", c.User)
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, bytes.TrimSpace(b), nil
}

// call is a reading API call as a tool's answer; a 2xx is ok, the body the result.
func (c *Client) call(ctx context.Context, method, path string, body any) (*mcp.CallToolResult, any, error) {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, nil, err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, rd)
	if err != nil {
		return nil, nil, err
	}
	if len(c.User) > 0 {
		req.Header.Set("X-Pack-User", c.User)
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	r := result{OK: resp.StatusCode >= 200 && resp.StatusCode < 300, Status: resp.StatusCode, Result: bytes.TrimSpace(b)}
	text, _ := json.MarshalIndent(r, "", "  ")
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(text)}}, IsError: !r.OK}, nil, nil
}

// sse reads an SSE stream to its end: the data of every event, in order.
func (c *Client) sse(ctx context.Context, path string) ([]json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.Base+path, nil)
	if err != nil {
		return nil, err
	}
	if len(c.User) > 0 {
		req.Header.Set("X-Pack-User", c.User)
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GET %s: %d: %s", path, resp.StatusCode, bytes.TrimSpace(b))
	}
	var out []json.RawMessage
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			out = append(out, json.RawMessage(data))
		}
	}
	return out, sc.Err()
}

// rel encodes a release the API's way: <project>:<path>, the path URL-escaped.
func rel(release string) (string, error) {
	proj, path, ok := strings.Cut(release, ":")
	if !ok || len(proj) == 0 || len(path) == 0 {
		return "", fmt.Errorf("a release is <project>:<path> (dev:releases/prod/db): %q", release)
	}
	return proj + ":" + url.PathEscape(path), nil
}

type relIn struct {
	Release string `json:"release" jsonschema:"the release, <project>:<path> (dev:releases/prod/db)"`
}

type logsIn struct {
	Release string `json:"release" jsonschema:"the release, <project>:<path>"`
	Key     string `json:"key,omitempty" jsonschema:"only this key's logs"`
	Tail    string `json:"tail,omitempty" jsonschema:"this many lines of each log"`
}

type journalIn struct {
	Release string `json:"release" jsonschema:"the release, <project>:<path>"`
	Saga    string `json:"saga,omitempty" jsonschema:"the saga's id (journal without it lists the sagas)"`
}

type requestIn struct {
	Release string `json:"release" jsonschema:"the release, <project>:<path>"`
	Action  string `json:"action" jsonschema:"apply | rollback | delete | run | refresh | discover | test"`
	Run     string `json:"run,omitempty" jsonschema:"action run: the action of the pack to run"`
	To      int    `json:"to,omitempty" jsonschema:"action rollback: the revision to go back to"`
}

type jobIn struct {
	ID string `json:"id" jsonschema:"the job's id (j-…), as a request or plan answer names it"`
}

// New is the operator's MCP server (К10).
func New(c *Client, version string) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "pack-operator", Version: version},
		&mcp.ServerOptions{Instructions: Instructions})

	op := func(name, desc, path string) {
		mcp.AddTool(srv, &mcp.Tool{Name: name, Description: desc},
			func(ctx context.Context, _ *mcp.CallToolRequest, in relIn) (*mcp.CallToolResult, any, error) {
				id, err := rel(in.Release)
				if err != nil {
					return nil, nil, err
				}
				return c.call(ctx, "GET", "/api/releases/"+id+path, nil)
			})
	}
	op("next", "Where the release is and what to run next: {state, blocking, next} — new, unrefreshed, unfilled, invalid, failed, planned, applied.", "/next")
	op("check", "Check the release's values against the pack: its schema, its rules, the environment.", "/check")
	op("status", "How the installation is: the version, drift from the record, the health of each key.", "/status")
	op("history", "The revisions of the installation: every saga that ended, and the removals.", "/history")

	mcp.AddTool(srv, &mcp.Tool{Name: "logs", Description: "The logs of the installed keys, by instance, from their targets."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in logsIn) (*mcp.CallToolResult, any, error) {
			id, err := rel(in.Release)
			if err != nil {
				return nil, nil, err
			}
			q := url.Values{}
			if len(in.Key) > 0 {
				q.Set("key", in.Key)
			}
			if len(in.Tail) > 0 {
				q.Set("tail", in.Tail)
			}
			path := "/api/releases/" + id + "/logs"
			if len(q) > 0 {
				path += "?" + q.Encode()
			}
			return c.call(ctx, "GET", path, nil)
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "journal", Description: "The sagas of the release, or one saga's events: what each stage did and said."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in journalIn) (*mcp.CallToolResult, any, error) {
			id, err := rel(in.Release)
			if err != nil {
				return nil, nil, err
			}
			if len(in.Saga) == 0 {
				return c.call(ctx, "GET", "/api/releases/"+id+"/sagas", nil)
			}
			events, err := c.sse(ctx, "/api/releases/"+id+"/sagas/"+url.PathEscape(in.Saga)+"/events")
			if err != nil {
				return nil, nil, err
			}
			text, _ := json.MarshalIndent(result{OK: true, Result: mustJSON(events)}, "", "  ")
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(text)}}}, nil, nil
		})

	// wait is how long plan and request watch the request's page for the state they answer.
	mcp.AddTool(srv, &mcp.Tool{Name: "plan", Description: "What an apply would do: the steps, the risks, the plan's digest. Under run.approve: plan the request then waits for a person's approval — say so and stop."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in relIn) (*mcp.CallToolResult, any, error) {
			id, err := rel(in.Release)
			if err != nil {
				return nil, nil, err
			}
			page, err := c.submit(ctx, id, map[string]any{"action": "plan"})
			if err != nil {
				return nil, nil, err
			}
			page, err = c.wait(ctx, page, "waiting_approval", "done", "failed", "stale")
			if err != nil {
				return nil, nil, err
			}
			text, _ := json.MarshalIndent(result{OK: page.State == "waiting_approval" || page.State == "done", Result: mustJSON(page)}, "", "  ")
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(text)}}}, nil, nil
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "request", Description: "Queue an action (apply, rollback, delete, run, refresh, discover, test) as the release's request: the policy plans it, a person approves, a worker runs it. The answer is the request; job reads its jobs."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in requestIn) (*mcp.CallToolResult, any, error) {
			id, err := rel(in.Release)
			if err != nil {
				return nil, nil, err
			}
			switch in.Action {
			case "apply", "rollback", "delete", "run", "refresh", "discover", "test":
			default:
				return nil, nil, fmt.Errorf("action %q: apply | rollback | delete | run | refresh | discover | test", in.Action)
			}
			body := map[string]any{"action": in.Action, "params": map[string]any{}}
			if len(in.Run) > 0 {
				body["params"].(map[string]any)["run"] = in.Run
			}
			if in.To > 0 {
				body["params"].(map[string]any)["to"] = in.To
			}
			page, err := c.submit(ctx, id, body)
			if err != nil {
				return nil, nil, err
			}
			text, _ := json.MarshalIndent(result{OK: true, Result: mustJSON(page)}, "", "  ")
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(text)}}}, nil, nil
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "job", Description: "A job: its state, exit and result, and the events it said, in order."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in jobIn) (*mcp.CallToolResult, any, error) {
			status, job, err := c.get(ctx, "/api/jobs/"+url.PathEscape(in.ID))
			if err != nil {
				return nil, nil, err
			}
			if status != http.StatusOK {
				text, _ := json.MarshalIndent(result{Status: status, Result: job}, "", "  ")
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(text)}}, IsError: true}, nil, nil
			}
			events, err := c.sse(ctx, "/api/jobs/"+url.PathEscape(in.ID)+"/events")
			if err != nil {
				return nil, nil, err
			}
			text, _ := json.MarshalIndent(result{OK: true, Result: mustJSON(map[string]any{
				"job": job, "events": events})}, "", "  ")
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(text)}}}, nil, nil
		})
	return srv
}

// requestPage is what a request's page says the tools wait on.
type requestPage struct {
	ID     string `json:"id"`
	State  string `json:"state"`
	Digest string `json:"digest"`
	SHA    string `json:"sha"`
}

// submit posts a request and answers its id.
func (c *Client) submit(ctx context.Context, relID string, body map[string]any) (requestPage, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", c.Base+"/api/releases/"+relID+"/requests", bytes.NewReader(mustJSON(body)))
	if err != nil {
		return requestPage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if len(c.User) > 0 {
		req.Header.Set("X-Pack-User", c.User)
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return requestPage{}, err
	}
	defer resp.Body.Close()
	var page requestPage
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return requestPage{}, err
	}
	if resp.StatusCode >= 300 {
		return page, fmt.Errorf("POST the request: %d", resp.StatusCode)
	}
	return page, nil
}

// wait polls the request's page until its state is one of states (five minutes at most).
func (c *Client) wait(ctx context.Context, page requestPage, states ...string) (requestPage, error) {
	deadline := time.Now().Add(5 * time.Minute)
	for {
		req, err := http.NewRequestWithContext(ctx, "GET", c.Base+"/api/requests/"+page.ID, nil)
		if err != nil {
			return page, err
		}
		if len(c.User) > 0 {
			req.Header.Set("X-Pack-User", c.User)
		}
		hc := c.HTTP
		if hc == nil {
			hc = http.DefaultClient
		}
		resp, err := hc.Do(req)
		if err != nil {
			return page, err
		}
		var cur requestPage
		_ = json.NewDecoder(resp.Body).Decode(&cur)
		resp.Body.Close()
		if len(cur.State) > 0 {
			page = cur
		}
		for _, s := range states {
			if page.State == s {
				return page, nil
			}
		}
		if time.Now().After(deadline) {
			return page, fmt.Errorf("the request %s is %s still", page.ID, page.State)
		}
		select {
		case <-ctx.Done():
			return page, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// mustJSON marshals what always marshals.
func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`null`)
	}
	return b
}
