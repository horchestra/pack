// Package mcpserver is pack's MCP server for the operator of a release: the commands a model needs to take a release
// from nothing to healthy and through a failure — next, questions, answer, check, plan, apply, diagnose, rollback,
// status, run — each run as pack itself runs it, with --json, and answered with what --json says: the same
// structures, no format of its own. What a person must give is not a tool's: no --approve, no secret's value (a
// password is a reference, name/key, and pack answer refuses anything else).
//
// The core knows nothing of MCP: this package runs pack, it is not linked into it but by cmd/mcp.go.
package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Instructions are what the server tells the model of how to use it.
const Instructions = `Tools of pack for one release. Loop: next — it says the state (new, unrefreshed, unfilled,
invalid, failed, planned, applied) and the commands to run; do what it says with these tools, then next again, until
applied. Values: questions lists what to answer, with the answers the pack offers; answer writes one. A password is a
reference {name, key} to a secret kept elsewhere: never a value. apply refused with needs_approval is a plan with
risks: show it to a person, approving is theirs. A failure: diagnose, then diagnose with the remedy it offers.
run performs only the actions the pack names. When next says a person should look, stop and say so.`

// Server runs pack for the release in Dir.
type Server struct {
	Pack string   // the pack program
	Dir  string   // the release's directory
	Env  []string // the environment of pack; nil — this process's
	// Envname is --env of the release (env.<name>.yaml), "" — env.yaml.
	Envname string
}

// result is a tool's answer: what pack --json said, and when it failed, the last lines it said on the way.
type result struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"` // the command's --json document, or {error, next}
	Log    []string        `json:"log,omitempty"`    // the last lines of what it said on the way, when it failed
}

const logLines = 30

// call runs pack with args and --json in the release.
func (s *Server) call(ctx context.Context, args ...string) (*mcp.CallToolResult, any, error) {
	full := []string{"-C", s.Dir, "--json"}
	if len(s.Envname) > 0 {
		full = append(full, "--env", s.Envname)
	}
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, s.Pack, full...)
	cmd.Env = s.Env
	if cmd.Env == nil {
		cmd.Env = os.Environ() // the zero Server's: pack's own, never an empty one
	}
	var out, said bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &said
	err := cmd.Run()
	r := result{OK: err == nil}
	if b := bytes.TrimSpace(out.Bytes()); len(b) > 0 && json.Valid(b) {
		r.Result = b
	}
	if err != nil {
		if _, exited := err.(*exec.ExitError); !exited {
			return nil, nil, fmt.Errorf("pack %s: %w", strings.Join(args, " "), err)
		}
		if tail := strings.TrimSpace(said.String()); len(tail) > 0 {
			lines := strings.Split(tail, "\n")
			if len(lines) > logLines {
				lines = lines[len(lines)-logLines:]
			}
			r.Log = lines
		}
	}
	text, _ := json.MarshalIndent(r, "", "  ")
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(text)}}, IsError: !r.OK}, nil, nil
}

type none struct{}

type answerIn struct {
	Answers map[string]any `json:"answers" jsonschema:"the answers, by the pointer of the setting (as questions gives it): a value of the setting's type; a password is {\"name\": …, \"key\": …}, a reference to a secret"`
}

type diagnoseIn struct {
	Remedy string `json:"remedy,omitempty" jsonschema:"the id of a way out diagnose offered (r1, r2 …) to take it; none — say the case"`
}

type rollbackIn struct {
	To     int  `json:"to,omitempty" jsonschema:"the revision to go back to (pack history); none — undo an unfinished saga, or the previous version"`
	DryRun bool `json:"dry_run,omitempty" jsonschema:"say what would be done, do nothing"`
}

type runIn struct {
	Action string            `json:"action,omitempty" jsonschema:"the action to run; none — list the actions of the pack and their parameters"`
	Key    string            `json:"key,omitempty" jsonschema:"the key to run it on, when several keys have the action"`
	Params map[string]string `json:"params,omitempty" jsonschema:"the parameters, by name"`
	DryRun bool              `json:"dry_run,omitempty" jsonschema:"say what would be run, run nothing"`
}

// New is the MCP server of the release.
func New(s *Server, version string) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "pack", Version: version}, &mcp.ServerOptions{Instructions: Instructions})
	simple := func(name, desc string, args ...string) {
		mcp.AddTool(srv, &mcp.Tool{Name: name, Description: desc}, func(ctx context.Context, _ *mcp.CallToolRequest, _ none) (*mcp.CallToolResult, any, error) {
			return s.call(ctx, args...)
		})
	}
	simple("next", "Where the release is and what to run next: {state, blocking, next}.", "next")
	simple("questions", "The settings to answer: required ones not given and ones the pack suggests a value for, with the answers offered.", "fill")
	simple("check", "Check the values against the pack: its schema, its rules, the environment.", "check")
	simple("plan", "What an apply would do: the steps, the risks, the plan's digest.", "plan")
	simple("apply", "Run the saga to what the release wants. A plan with risks is refused with needs_approval: a person approves it.", "apply")
	simple("status", "How the installation is: the version, drift from the record, the health of each key.", "status")

	mcp.AddTool(srv, &mcp.Tool{Name: "answer", Description: "Write answers into values.yaml, said to be a model's. An answer the schema or a rule refuses is not written."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in answerIn) (*mcp.CallToolResult, any, error) {
			if len(in.Answers) == 0 {
				return nil, nil, fmt.Errorf("answers: none given")
			}
			f, err := os.CreateTemp("", "pack-answers-*.json")
			if err != nil {
				return nil, nil, err
			}
			defer os.Remove(f.Name())
			if err := json.NewEncoder(f).Encode(in.Answers); err != nil {
				_ = f.Close()
				return nil, nil, err
			}
			if err := f.Close(); err != nil {
				return nil, nil, err
			}
			return s.call(ctx, "answer", "--answers", f.Name(), "--source", "llm")
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "diagnose", Description: "What failed and why, and the ways out; with remedy, take one."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in diagnoseIn) (*mcp.CallToolResult, any, error) {
			args := []string{"diagnose"}
			if len(in.Remedy) > 0 {
				args = append(args, "--apply", in.Remedy)
			}
			return s.call(ctx, args...)
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "rollback", Description: "Take the installation back: an unfinished saga, the previous version, or revision to. Risks are refused with needs_approval."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in rollbackIn) (*mcp.CallToolResult, any, error) {
			args := []string{"rollback"}
			if in.To > 0 {
				args = append(args, "--to", fmt.Sprint(in.To))
			}
			if in.DryRun {
				args = append(args, "--dry-run")
			}
			return s.call(ctx, args...)
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "run", Description: "Run an action of the pack (backup, restore …) on the installation, or list them. Nothing else may be run."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in runIn) (*mcp.CallToolResult, any, error) {
			args := []string{"run"}
			if len(in.Action) > 0 {
				args = append(args, in.Action)
			}
			if len(in.Key) > 0 {
				args = append(args, "--key", in.Key)
			}
			for _, k := range slices.Sorted(maps.Keys(in.Params)) {
				args = append(args, "--param", k+"="+in.Params[k])
			}
			if in.DryRun {
				args = append(args, "--dry-run")
			}
			return s.call(ctx, args...)
		})
	return srv
}
