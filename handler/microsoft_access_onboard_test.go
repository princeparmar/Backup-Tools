package handler

import "testing"

func TestAccessCheckItemKeyHelpersStillCompile(t *testing.T) {
	// Onboard mode helpers were removed in favor of system_job_id materialize.
	// Keep a trivial package test so the file remains valid.
	if serviceNameFromMethod("outlook") != "outlook" {
		t.Fatalf("unexpected service name")
	}
}
