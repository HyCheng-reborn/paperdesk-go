package probe_test

// 复核 P2（空凭据误报就绪）的回归测试：Readiness 与实际执行共享同一组凭据规则。

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/wangle201210/go-rag/server/core/probe"
)

func TestCredentials_SharedRules(t *testing.T) {
	const (
		validName  = "PROBE_FIX2_CREDENTIAL_VALID"
		emptyName  = "PROBE_FIX2_CREDENTIAL_EMPTY"
		blankName  = "PROBE_FIX2_CREDENTIAL_BLANK"
		absentName = "PROBE_FIX2_CREDENTIAL_ABSENT"
		secretMark = "placeholder-secret-do-not-leak"
	)
	t.Setenv(validName, secretMark)
	t.Setenv(emptyName, "")
	t.Setenv(blankName, "   ")
	// 确保 absentName 在当前进程中确实未设置（只清理该名字，测试结束后按原状恢复）。
	original, hadValue := os.LookupEnv(absentName)
	if err := os.Unsetenv(absentName); err != nil {
		t.Fatalf("unsetenv %s: %v", absentName, err)
	}
	t.Cleanup(func() {
		if hadValue {
			_ = os.Setenv(absentName, original)
		}
	})

	cases := []struct {
		name    string
		envs    []string
		wantErr bool
	}{
		{"not_declared", nil, true},
		{"blank_name", []string{"   "}, true},
		{"two_declarations", []string{validName, absentName}, true},
		{"duplicate_declaration", []string{validName, validName}, true},
		{"absent_variable", []string{absentName}, true},
		{"empty_value", []string{emptyName}, true},
		{"whitespace_value", []string{blankName}, true},
		{"valid_single", []string{validName}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			creds := probe.Credentials{EnvNames: tc.envs}
			err := creds.Validate()
			value, resolveErr := creds.Resolve()
			if tc.wantErr {
				if !errors.Is(err, probe.ErrMissingCredentials) {
					t.Fatalf("Validate() = %v, want ErrMissingCredentials", err)
				}
				if !errors.Is(resolveErr, probe.ErrMissingCredentials) {
					t.Fatalf("Resolve() = %v, want the same sentinel as Validate (shared rules)", resolveErr)
				}
				if strings.Contains(err.Error(), secretMark) {
					t.Fatalf("Validate() leaks the credential value: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate() unexpected error: %v", err)
			}
			if resolveErr != nil {
				t.Fatalf("Resolve() unexpected error: %v", resolveErr)
			}
			if value != secretMark {
				t.Fatal("Resolve() did not return the declared variable's value")
			}
		})
	}
}

// TestCredentials_PresenceTreatsEmptyAsMissing：Presence/Missing 与 Validate 的
// "空值等同缺失"口径一致，且同样不回显值。
func TestCredentials_PresenceTreatsEmptyAsMissing(t *testing.T) {
	const (
		validName = "PROBE_FIX2_PRESENCE_VALID"
		emptyName = "PROBE_FIX2_PRESENCE_EMPTY"
		blankName = "PROBE_FIX2_PRESENCE_BLANK"
	)
	t.Setenv(validName, "placeholder-secret-do-not-leak")
	t.Setenv(emptyName, "")
	t.Setenv(blankName, "  ")

	creds := probe.Credentials{EnvNames: []string{validName, emptyName, blankName, emptyName}}
	presence := creds.Presence()
	if !presence[validName] {
		t.Fatalf("presence = %v, want the non-empty variable to be usable", presence)
	}
	if presence[emptyName] || presence[blankName] {
		t.Fatalf("presence = %v, want empty/whitespace values to count as unusable", presence)
	}
	missing := creds.Missing()
	if len(missing) != 2 || missing[0] != emptyName || missing[1] != blankName {
		t.Fatalf("Missing() = %v, want [%s %s] in declaration order and deduplicated", missing, emptyName, blankName)
	}
	for _, name := range missing {
		if strings.Contains(name, "placeholder-secret") {
			t.Fatalf("Missing() leaked a credential value: %v", missing)
		}
	}
}
