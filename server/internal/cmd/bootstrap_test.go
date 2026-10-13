package cmd

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func contains(s []string, substr string) bool {
	for _, v := range s {
		if v == substr {
			return true
		}
	}
	return false
}

// TestRunInitSteps_AllSuccess 验证所有步骤成功时返回 nil。
func TestRunInitSteps_AllSuccess(t *testing.T) {
	var order []string
	steps := []InitStep{
		{Name: "step1", Fn: func() error { order = append(order, "step1"); return nil }, Cleanup: func() error { order = append(order, "cleanup1"); return nil }},
		{Name: "step2", Fn: func() error { order = append(order, "step2"); return nil }, Cleanup: func() error { order = append(order, "cleanup2"); return nil }},
	}

	err := RunInitSteps(steps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := []string{"step1", "step2"}
	if !reflect.DeepEqual(order, expected) {
		t.Fatalf("unexpected order: got %v, want %v", order, expected)
	}
}

// TestRunInitSteps_FailureStopsAndCleans 验证失败时停止并逆序清理。
func TestRunInitSteps_FailureStopsAndCleans(t *testing.T) {
	var order []string
	steps := []InitStep{
		{Name: "step1", Fn: func() error { order = append(order, "step1"); return nil }, Cleanup: func() error { order = append(order, "cleanup1"); return nil }},
		{Name: "step2", Fn: func() error { order = append(order, "step2"); return errors.New("step2 error") }, Cleanup: func() error { return nil }},
		{Name: "step3", Fn: func() error { order = append(order, "step3"); return nil }, Cleanup: func() error { return nil }},
	}

	err := RunInitSteps(steps)
	if err == nil {
		t.Fatal("expected error")
	}

	// step3 不应执行
	if contains(order, "step3") {
		t.Fatal("step3 should not run")
	}

	// 逆序清理：step1 已执行，应被清理
	expected := []string{"step1", "step2", "cleanup1"}
	if !reflect.DeepEqual(order, expected) {
		t.Fatalf("unexpected order: got %v, want %v", order, expected)
	}
}

// TestRunInitSteps_CleanupErrorPreserved 验证清理错误被保留。
func TestRunInitSteps_CleanupErrorPreserved(t *testing.T) {
	steps := []InitStep{
		{Name: "step1", Fn: func() error { return nil }, Cleanup: func() error { return errors.New("cleanup1 error") }},
		{Name: "step2", Fn: func() error { return errors.New("step2 error") }, Cleanup: func() error { return nil }},
	}

	err := RunInitSteps(steps)
	if err == nil {
		t.Fatal("expected error")
	}

	// 验证包含原始错误
	if !strings.Contains(err.Error(), "step2 error") {
		t.Fatalf("should contain step2 error, got: %v", err)
	}

	// 验证清理错误被保留
	if !strings.Contains(err.Error(), "cleanup1 error") {
		t.Fatalf("should contain cleanup error, got: %v", err)
	}
}

// TestRunInitSteps_EmptySteps 验证空步骤列表返回 nil。
func TestRunInitSteps_EmptySteps(t *testing.T) {
	err := RunInitSteps(nil)
	if err != nil {
		t.Fatalf("unexpected error with empty steps: %v", err)
	}
}

// TestRunInitSteps_FirstStepFails 验证第一步失败时不清理任何内容。
func TestRunInitSteps_FirstStepFails(t *testing.T) {
	var order []string
	steps := []InitStep{
		{Name: "step1", Fn: func() error { order = append(order, "step1"); return errors.New("step1 error") }, Cleanup: func() error { order = append(order, "cleanup1"); return nil }},
		{Name: "step2", Fn: func() error { order = append(order, "step2"); return nil }, Cleanup: func() error { return nil }},
	}

	err := RunInitSteps(steps)
	if err == nil {
		t.Fatal("expected error")
	}

	// 第一步失败，没有已完成的步骤，不应有清理
	if contains(order, "cleanup1") {
		t.Fatal("cleanup1 should not run when step1 fails")
	}
	if contains(order, "step2") {
		t.Fatal("step2 should not run when step1 fails")
	}
}

// TestRunInitSteps_ErrorContainsStageName 验证错误包含阶段名称。
func TestRunInitSteps_ErrorContainsStageName(t *testing.T) {
	steps := []InitStep{
		{Name: "myStage", Fn: func() error { return errors.New("my error") }, Cleanup: func() error { return nil }},
	}

	err := RunInitSteps(steps)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "stage[myStage]") {
		t.Fatalf("error should contain stage name, got: %v", err)
	}
}
