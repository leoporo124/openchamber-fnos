package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectRuntimeCLI(t *testing.T) {
	tests := []struct {
		name    string
		version string
		bin     map[string]string
		cliFile string
		wantErr string
	}{
		{name: "legacy layout", version: "2.7.2", bin: map[string]string{"openchamber": "bin/cli.js"}, cliFile: "bin/cli.js"},
		{name: "compiled layout", version: "2.7.3", bin: map[string]string{"openchamber": "build/bin/cli.js"}, cliFile: "build/bin/cli.js"},
		{name: "relative prefix", version: "2.7.3", bin: map[string]string{"openchamber": "./build/bin/cli.js"}, cliFile: "build/bin/cli.js"},
		{name: "wrong file present", version: "2.7.3", bin: map[string]string{"openchamber": "build/bin/cli.js"}, cliFile: "bin/cli.js", wantErr: "运行包入口缺失"},
		{name: "undeclared", version: "2.7.3", cliFile: "build/bin/cli.js", wantErr: "未声明"},
		{name: "outside package", version: "2.7.3", bin: map[string]string{"openchamber": "../cli.js"}, wantErr: "路径无效"},
		{name: "absolute path", version: "2.7.3", bin: map[string]string{"openchamber": "/tmp/cli.js"}, wantErr: "路径无效"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			const webDir = "node_modules/@openchamber/web/"
			const codeDir = "node_modules/@opencode/cli/"
			manifest, err := json.Marshal(map[string]any{
				"name": webPackageName, "version": tt.version, "bin": tt.bin,
				"dependencies": map[string]string{"@opencode/client": "2.3.2", "@opencode/schema": "2.3.2"},
				"engines":      map[string]string{"node": ">=24.14.0"},
			})
			if err != nil {
				t.Fatal(err)
			}
			files := map[string]string{
				webDir + "package.json":      string(manifest),
				webDir + "dist/index.html":   "<html></html>",
				codeDir + "package.json":     `{"name":"@opencode/cli","version":"2.3.2"}`,
				codeDir + "bin/opencode.exe": "opencode",
			}
			if tt.cliFile != "" {
				files[webDir+tt.cliFile] = "#!/usr/bin/env node\n"
			}
			for name, content := range files {
				file := filepath.Join(dir, filepath.FromSlash(name))
				if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			launch, err := inspectRuntime(dir)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("inspectRuntime error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			wantPath := filepath.Join(dir, filepath.FromSlash(webDir+tt.cliFile))
			if launch.cliPath != wantPath || launch.nodeEngine != ">=24.14.0" {
				t.Fatalf("inspectRuntime = %+v, want CLI %q and Node >=24.14.0", launch, wantPath)
			}
		})
	}
}
