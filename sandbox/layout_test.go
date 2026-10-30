package sandbox

import (
	"errors"
	"testing"
)

// plainRuntime implements Runtime but not LayoutProvider.
type plainRuntime struct{ StubRuntime }

type layoutRuntime struct {
	plainRuntime
	layout Layout
	err    error
}

func (l *layoutRuntime) Layout(CommandRequest) (Layout, error) { return l.layout, l.err }

func TestLayoutOf(t *testing.T) {
	reg, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	custom := Layout{Workspace: "/srv/ws", Home: "/srv/home"}
	boom := errors.New("boom")
	tests := []struct {
		name    string
		rt      Runtime
		want    Layout
		wantErr error
	}{
		{"runtime without LayoutProvider", &plainRuntime{}, ContainerLayout(), nil},
		{"stub runtime", NewStubRuntime(), ContainerLayout(), nil},
		{"runtime with layout", &layoutRuntime{layout: custom}, custom, nil},
		{"layout error", &layoutRuntime{err: boom}, Layout{}, boom},
		{"manager delegates", NewManager(&layoutRuntime{layout: custom}, reg), custom, nil},
		{"manager over plain runtime", NewManager(&plainRuntime{}, reg), ContainerLayout(), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := LayoutOf(tt.rt, CommandRequest{TaskID: "t1"})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("layout = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestContainerLayout(t *testing.T) {
	if got := ContainerLayout(); got.Workspace != "/workspace" || got.Home != HomeDir {
		t.Errorf("ContainerLayout = %+v", got)
	}
}

var (
	_ Runtime        = (*layoutRuntime)(nil)
	_ LayoutProvider = (*Manager)(nil)
)
