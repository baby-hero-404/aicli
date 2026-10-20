package profile

import (
	"strings"
	"testing"
)

func TestLookup_KnownKey(t *testing.T) {
	p, ok := Lookup("claude_code")
	if !ok {
		t.Fatal("expected claude_code to be known")
	}
	if p.Command != "claude" {
		t.Errorf("Command = %q, want %q", p.Command, "claude")
	}
	if len(p.StateDirs) != 1 || p.StateDirs[0] != ".claude" {
		t.Errorf("StateDirs = %v, want [.claude]", p.StateDirs)
	}
}

// TestLookup_Antigravity_PBeforePromptArg guards the fix for a
// confirmed bug: agy's flag parser is Go stdlib `flag`, where -p is a value
// flag — whatever token immediately follows -p becomes its value. A boolean
// flag placed after -p (e.g. --dangerously-skip-permissions) gets consumed
// as the prompt text instead of the actual instruction, confirmed from a
// real run's captured output (see docs/guides/antigravity-cli-headless.md
// and profile.go's antigravity comment). Boolean flags must precede -p.
func TestLookup_Antigravity_PBeforePromptArg(t *testing.T) {
	p, ok := Lookup("antigravity")
	if !ok {
		t.Fatal("expected antigravity to be known")
	}
	if p.Command != "agy" {
		t.Errorf("Command = %q, want %q (the real headless CLI binary)", p.Command, "agy")
	}
	pIdx := -1
	for i, a := range p.Args {
		if a == "-p" {
			pIdx = i
		}
	}
	if pIdx == -1 {
		t.Fatalf("Args %v: expected a -p flag", p.Args)
	}
	if pIdx+1 >= len(p.Args) || strings.HasPrefix(p.Args[pIdx+1], "-") {
		t.Errorf("Args %v: the token immediately after -p must be the prompt text, not another flag", p.Args)
	}
}

func TestLookup_UnknownKey(t *testing.T) {
	_, ok := Lookup("not_real")
	if ok {
		t.Fatal("expected not_real to be unknown")
	}
}

func TestLookup_ReturnsIndependentCopy(t *testing.T) {
	p, _ := Lookup("claude_code")
	p.Args[0] = "mutated"
	p.Args = append(p.Args, "extra")

	again, _ := Lookup("claude_code")
	if again.Args[0] == "mutated" || len(again.Args) == len(p.Args) {
		t.Errorf("registry was modified through a Lookup result: %v", again.Args)
	}
}

func TestList_ReturnsIndependentCopy(t *testing.T) {
	all := List()
	if len(all) != len(profiles) {
		t.Fatalf("List() has %d entries, want %d", len(all), len(profiles))
	}
	delete(all, "claude_code")
	all["openai_codex"].Args[0] = "mutated"

	if _, ok := Lookup("claude_code"); !ok {
		t.Error("deleting from List() result removed a registry entry")
	}
	if p, _ := Lookup("openai_codex"); p.Args[0] == "mutated" {
		t.Error("mutating a List() result changed the registry")
	}
}

func TestLookup_Unknown(t *testing.T) {
	if _, ok := Lookup("nope"); ok {
		t.Error("unknown key must not be found")
	}
}

func TestResumeArgs(t *testing.T) {
	tests := []struct {
		name, ref, session string
		want               []string
	}{
		{"claude", "claude_code", "s1", []string{"--resume", "s1"}},
		{"antigravity", "antigravity", "s1", []string{"--conversation", "s1"}},
		{"codex resume not supported yet", "openai_codex", "s1", nil},
		{"empty session", "claude_code", "", nil},
		{"unknown ref", "custom", "s1", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResumeArgs(tt.ref, tt.session)
			if strings.Join(got, " ") != strings.Join(tt.want, " ") {
				t.Errorf("ResumeArgs = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestProfiles_StateLayout(t *testing.T) {
	tests := []struct {
		ref     string
		dirs    []string
		wantEnv map[string]string
	}{
		{"claude_code", []string{".claude"}, map[string]string{"CLAUDE_CONFIG_DIR": "/home/agent/.claude"}},
		{"openai_codex", []string{".codex"}, nil},
		{"antigravity", []string{".gemini"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			p, ok := Lookup(tt.ref)
			if !ok {
				t.Fatal("unknown profile")
			}
			if strings.Join(p.StateDirs, ",") != strings.Join(tt.dirs, ",") {
				t.Errorf("StateDirs = %v, want %v", p.StateDirs, tt.dirs)
			}
			env := p.Env("/home/agent")
			if len(env) != len(tt.wantEnv) {
				t.Fatalf("Env = %v, want %v", env, tt.wantEnv)
			}
			for k, v := range tt.wantEnv {
				if env[k] != v {
					t.Errorf("Env[%s] = %q, want %q", k, env[k], v)
				}
			}
			// Every StateEnv target must be one of the StateDirs, or state would land outside
			// the mounted per-account directory.
			for _, rel := range p.StateEnv {
				found := false
				for _, d := range p.StateDirs {
					if rel == d {
						found = true
					}
				}
				if !found {
					t.Errorf("StateEnv target %q is not a StateDir", rel)
				}
			}
		})
	}
}

func TestLookup_StateFieldsAreCopies(t *testing.T) {
	p, _ := Lookup("claude_code")
	p.StateDirs[0] = "mutated"
	p.StateEnv["CLAUDE_CONFIG_DIR"] = "mutated"
	again, _ := Lookup("claude_code")
	if again.StateDirs[0] != ".claude" || again.StateEnv["CLAUDE_CONFIG_DIR"] != ".claude" {
		t.Error("registry state fields were modified through a Lookup result")
	}
}

func TestProfiles_AuthCheckIsReal(t *testing.T) {
	// A version command always succeeds, so it can never detect a logged-out account.
	for ref, p := range List() {
		if strings.Contains(p.AuthCheckCommand, "--version") {
			t.Errorf("%s: AuthCheckCommand %q does not verify login", ref, p.AuthCheckCommand)
		}
	}
}
