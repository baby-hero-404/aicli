package sandbox

import (
	"testing"
)

func TestValidateCommand(t *testing.T) {
	tests := []struct {
		name    string
		command []string
		wantErr bool
	}{
		{name: "empty", command: nil, wantErr: true},
		{name: "safe", command: []string{"go", "test", "./..."}, wantErr: false},
		{name: "blocked rm workspace", command: []string{"bash", "-lc", "rm -rf /workspace"}, wantErr: true},
		{name: "blocked rm root", command: []string{"bash", "-lc", "rm -rf /"}, wantErr: true},
		{name: "allowed subpath cleanup", command: []string{"bash", "-lc", "cd '/workspace' && claude code; rm -rf '/workspace/.cache'; exit $status"}, wantErr: false},
		{name: "allowed node_modules cleanup", command: []string{"bash", "-lc", "rm -rf node_modules"}, wantErr: false},
		{name: "blocked mkfs", command: []string{"mkfs.ext4", "/dev/sda"}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCommand(tc.command)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateCommand() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateExecutionPolicy(t *testing.T) {
	secrets := map[string]string{"TOKEN": "secret"}
	tests := []struct {
		name     string
		req      CommandRequest
		resolved string
		wantErr  bool
	}{
		{name: "default resolved to none", req: CommandRequest{}, resolved: NetworkModeNone, wantErr: false},
		{name: "bridge without secrets", req: CommandRequest{NetworkMode: NetworkModeBridge}, resolved: "bridge", wantErr: false},
		{name: "bridge with secrets", req: CommandRequest{NetworkMode: NetworkModeBridge, SecretEnv: secrets}, resolved: "bridge", wantErr: true},
		{name: "unsupported network", req: CommandRequest{NetworkMode: "host"}, resolved: "host", wantErr: true},
		// The mismatch the policy previously missed: NetworkModeDefault
		// requested, but the runtime resolves it to an egress-enabled mode
		// because DisableNetworking is false. Secrets must be blocked.
		{name: "default resolved to bridge with secrets", req: CommandRequest{NetworkMode: NetworkModeDefault, SecretEnv: secrets}, resolved: "bridge", wantErr: true},
		{name: "default resolved to none with secrets", req: CommandRequest{NetworkMode: NetworkModeDefault, SecretEnv: secrets}, resolved: NetworkModeNone, wantErr: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateExecutionPolicy(tc.req, tc.resolved)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateExecutionPolicy() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateMounts(t *testing.T) {
	tests := []struct {
		name    string
		req     CommandRequest
		wantErr bool
	}{
		{"empty", CommandRequest{}, false},
		{"valid", CommandRequest{
			CredentialFiles:  map[string]string{"/home/agent/.claude/.credentials.json": "x"},
			SessionMounts:    map[string]string{"/home/agent/.claude": "/var/lib/sessions/a"},
			ExtraCacheMounts: map[string]string{"/go/pkg/mod": "/cache/gomod"},
		}, false},
		{"credential traversal", CommandRequest{CredentialFiles: map[string]string{"/home/agent/.claude/../../etc/x": "x"}}, true},
		{"credential relative", CommandRequest{CredentialFiles: map[string]string{"home/agent/x": "x"}}, true},
		{"credential not clean", CommandRequest{CredentialFiles: map[string]string{"/home//agent/x": "x"}}, true},
		{"session target traversal", CommandRequest{SessionMounts: map[string]string{"/home/agent/../x": "/abs"}}, true},
		{"session source relative", CommandRequest{SessionMounts: map[string]string{"/home/agent/.c": "rel/dir"}}, true},
		{"cache target relative", CommandRequest{ExtraCacheMounts: map[string]string{"cache": "/abs"}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateMounts(tt.req); (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
