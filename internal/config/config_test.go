package config

import "testing"

func TestDefaultConfigEnablesCoreCodingTools(t *testing.T) {
	got := DefaultConfig()
	if !got.Tools.Read || !got.Tools.Write || !got.Tools.Edit || !got.Tools.Bash {
		t.Fatalf("core tools = %#v, want read/write/edit/bash enabled", got.Tools)
	}
	if got.Env.ShellPath != "/bin/bash" {
		t.Fatalf("default shell = %q, want /bin/bash", got.Env.ShellPath)
	}
}
