package scenario

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func TestScriptAssignAndBuiltins(t *testing.T) {
	code := `
		// Compute deterministic HMAC signature and parse JSON field
		let secret = vars.api_secret
		let payload = json_get('{"user":{"id":"u-42","role":"admin"}}', '$.user.id')
		vars.sig = hmac_sha256(secret, payload)
		vars.upper_id = upper(payload)
		vars["b64_id"] = base64_encode(payload)
		set("sum_val", (10 + 5) * 2 - 4 / 2)
		if (vars.upper_id == "U-42" && vars.sum_val == 28) {
			vars.branch = "matched"
		} else {
			vars.branch = "missed"
		}
		assert(contains(vars.sig, "") && len(vars.sig) == 64, "signature length")
	`

	prog, err := CompileScript(code, nil)
	if err != nil {
		t.Fatalf("CompileScript failed: %v", err)
	}

	env := &Env{Vars: make([][]byte, len(prog.OwnVars))}
	for i, name := range prog.OwnVars {
		if name == "api_secret" {
			env.Vars[i] = []byte("top-key")
		}
	}

	out, err := prog.ExecDebug(env)
	if err != nil {
		t.Fatalf("ExecDebug failed: %v", err)
	}
	if len(out.Asserts) != 1 || !out.Asserts[0].Passed {
		t.Fatalf("unexpected assert outcomes: %+v", out.Asserts)
	}

	mac := hmac.New(sha256.New, []byte("top-key"))
	mac.Write([]byte("u-42"))
	wantSig := hex.EncodeToString(mac.Sum(nil))

	if out.WrittenVars["sig"] != wantSig {
		t.Fatalf("sig = %q, want %q", out.WrittenVars["sig"], wantSig)
	}
	if out.WrittenVars["upper_id"] != "U-42" || out.WrittenVars["b64_id"] != "dS00Mg==" || out.WrittenVars["sum_val"] != "28" || out.WrittenVars["branch"] != "matched" {
		t.Fatalf("unexpected written vars: %+v", out.WrittenVars)
	}
}

func TestScriptAssertFailureAndDebugCollection(t *testing.T) {
	code := `
		vars.a = "ok"
		assert(vars.a == "nope", "must match nope")
		assert(1 == 1, "one is one")
	`
	prog, err := CompileScript(code, nil)
	if err != nil {
		t.Fatal(err)
	}
	env := &Env{Vars: make([][]byte, len(prog.OwnVars))}
	if err := prog.Exec(env); !errors.Is(err, ErrScriptAssert) {
		t.Fatalf("expected ErrScriptAssert from Exec, got %v", err)
	}
	dbg, err := prog.ExecDebug(&Env{Vars: make([][]byte, len(prog.OwnVars))})
	if err != nil {
		t.Fatalf("ExecDebug unexpected error: %v", err)
	}
	if len(dbg.Asserts) != 2 || dbg.Asserts[0].Passed || !dbg.Asserts[1].Passed {
		t.Fatalf("expected [fail, pass] in ExecDebug, got %+v", dbg.Asserts)
	}
}

func TestScriptSafetyBounds(t *testing.T) {
	t.Run("oversized source", func(t *testing.T) {
		big := strings.Repeat("let x = 1\n", MaxScriptBytes/8)
		if _, err := CompileScript(big, nil); err == nil {
			t.Fatal("expected error for oversized script")
		}
	})

	t.Run("string bomb limit", func(t *testing.T) {
		prog, err := CompileScript(`let s = random_string(100000); let b = s + s + s`, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := prog.Exec(nil); err == nil {
			t.Fatal("expected string size limit error")
		}
	})

	t.Run("op budget exceeded", func(t *testing.T) {
		var b strings.Builder
		for i := 0; i < 450; i++ {
			b.WriteString("let x = 1 + 2 + 3 + 4\n")
		}
		prog, err := CompileScript(b.String(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := prog.Exec(nil); err == nil || !strings.Contains(err.Error(), "上限") {
			t.Fatalf("expected op budget exceeded error, got %v", err)
		}
	})

	t.Run("extract assigned vars", func(t *testing.T) {
		names, err := ExtractAssignedVars(`
			vars.alpha = "1"
			env.beta = "2"
			vars["gamma"] = "3"
			set("delta", "4")
		`)
		if err != nil {
			t.Fatal(err)
		}
		if len(names) != 4 {
			t.Fatalf("got %v, want 4 assigned vars", names)
		}
	})
}

func TestCompileProtocolsAndScriptStep(t *testing.T) {
	t.Run("standalone script stays counted", func(t *testing.T) {
		def := &Definition{
			Version: 1,
			Name:    "script-only",
			Variables: map[string]Scalar{
				"key": "abc",
			},
			Steps: []Step{
				{
					ID:     "sc1",
					Script: &ScriptStep{Code: `vars.out = sha256(vars.key)`},
				},
			},
			Load: Load{
				Mode:   "vus",
				Stages: []Stage{{Duration: Duration(1), Target: 1}},
			},
		}
		plan, err := Compile(def, CompileOptions{})
		if err != nil {
			t.Fatalf("Compile standalone script: %v", err)
		}
		sc, ok := plan.Steps[0].(*ScriptStepPlan)
		if !ok {
			t.Fatalf("expected *ScriptStepPlan, got %T", plan.Steps[0])
		}
		if sc.IsGroup || plan.FlatSteps[0].IsGroup {
			t.Fatalf("expected standalone script step IsGroup=false, got step=%v flat=%v", sc.IsGroup, plan.FlatSteps[0].IsGroup)
		}
	})

	t.Run("mixed protocols and script marked group", func(t *testing.T) {
		def := &Definition{
			Version: 1,
			Name:    "multi-proto",
			Variables: map[string]Scalar{
				"secret": "k1",
			},
			Steps: []Step{
				{
					ID:     "sign",
					Script: &ScriptStep{Code: `vars.sig = hmac_sha256(vars.secret, "hello")`},
				},
				{
					ID: "ws1",
					WebSocket: &WebSocketStep{
						URL:          "wss://example.com/ws",
						Subprotocols: []string{"json"},
						Messages:     []WSMessage{{Type: "text", Data: `{"sig":"{{sig}}"}`}},
						RecvCount:    1,
					},
					Extract: []Extract{{Name: "ws_id", From: "jsonpath", Expr: "$.id"}},
				},
				{
					ID: "grpc1",
					GRPC: &GRPCStep{
						Target:           "grpcs://example.com:8443",
						Service:          "echo.EchoService",
						Method:           "Echo",
						Payload:          `{"msg":"{{ws_id}}"}`,
						PayloadEncoding:  "json",
						ResponseEncoding: "raw",
					},
				},
				{
					ID: "sse1",
					SSE: &SSEStep{
						URL:          "https://example.com/events",
						MaxEvents:    3,
						StopContains: "[DONE]",
					},
				},
			},
			Load: Load{
				Mode:   "vus",
				Stages: []Stage{{Duration: Duration(1), Target: 1}},
			},
		}
		plan, err := Compile(def, CompileOptions{})
		if err != nil {
			t.Fatalf("Compile mixed protocols: %v", err)
		}
		if len(plan.FlatSteps) != 4 {
			t.Fatalf("FlatSteps = %d, want 4", len(plan.FlatSteps))
		}
		sc := plan.Steps[0].(*ScriptStepPlan)
		if !sc.IsGroup || !plan.FlatSteps[0].IsGroup {
			t.Fatalf("expected script IsGroup=true alongside network steps, got %v / %v", sc.IsGroup, plan.FlatSteps[0].IsGroup)
		}
		ws := plan.Steps[1].(*WSStep)
		if ws.IsGroup || ws.URL.Static == nil || ws.URL.Static.Scheme != "wss" || !ws.URL.Static.TLS {
			t.Fatalf("unexpected compiled WSStep: %+v", ws)
		}
		gr := plan.Steps[2].(*GRPCStepPlan)
		if gr.FullMethod != "/echo.EchoService/Echo" || !gr.TLS || gr.Origin == nil || gr.Origin.Addr != "example.com:8443" {
			t.Fatalf("unexpected compiled GRPCStepPlan: %+v", gr)
		}
		se := plan.Steps[3].(*SSEStepPlan)
		if se.Method != "GET" || se.MaxEvents != 3 || string(se.StopContains) != "[DONE]" {
			t.Fatalf("unexpected compiled SSEStepPlan: %+v", se)
		}
	})
}
