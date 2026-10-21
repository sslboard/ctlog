package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func envOf(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestPathPrefersXDG(t *testing.T) {
	got, err := Path(envOf(map[string]string{"XDG_CONFIG_HOME": "/x", "HOME": "/h"}))
	if err != nil || got != filepath.Join("/x", "ctlog", "config.json") {
		t.Errorf("Path = %q, %v", got, err)
	}
	if runtime.GOOS != "windows" {
		got, _ = Path(envOf(map[string]string{"HOME": "/h"}))
		if got != filepath.Join("/h", ".config", "ctlog", "config.json") {
			t.Errorf("Path = %q", got)
		}
	}
	if _, err := Path(envOf(nil)); err == nil {
		t.Error("Path without HOME should fail")
	}
}

func TestSaveLoadRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ctlog", "config.json")

	cfg, err := Load(path)
	if err != nil || cfg != (Config{}) {
		t.Fatalf("Load missing = %+v, %v", cfg, err)
	}
	want := Config{APIKey: "sslb_live_x", Email: "a@example.com"}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	if got, err := Load(path); err != nil || got != want {
		t.Errorf("Load = %+v, %v", got, err)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(path)
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("file mode = %o, want 600", perm)
		}
		dirInfo, _ := os.Stat(filepath.Dir(path))
		if perm := dirInfo.Mode().Perm(); perm != 0o700 {
			t.Errorf("dir mode = %o, want 700", perm)
		}
	}
	if err := Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := Remove(path); err != nil {
		t.Errorf("second Remove = %v", err)
	}
}

func TestLoadRejectsBadJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(path, []byte("{"), 0o600)
	if _, err := Load(path); err == nil {
		t.Error("expected an error")
	}
}

func TestResolveKey(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{"XDG_CONFIG_HOME": dir}
	if key, src, err := ResolveKey(envOf(env)); key != "" || src != SourceNone || err != nil {
		t.Errorf("empty = %q %q %v", key, src, err)
	}
	Save(filepath.Join(dir, "ctlog", "config.json"), Config{APIKey: "from-file"})
	if key, src, _ := ResolveKey(envOf(env)); key != "from-file" || src != SourceFile {
		t.Errorf("file = %q %q", key, src)
	}
	env[EnvAPIKey] = " from-env "
	if key, src, _ := ResolveKey(envOf(env)); key != "from-env" || src != SourceEnv {
		t.Errorf("env = %q %q", key, src)
	}
	if key, src, err := ResolveKey(envOf(nil)); key != "" || src != SourceNone || err != nil {
		t.Errorf("no home = %q %q %v", key, src, err)
	}
}
