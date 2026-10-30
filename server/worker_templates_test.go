package server

import "testing"

func TestValidateWorkerTemplateBody(t *testing.T) {
	valid := "name={{.WorkerName}} proxy={{.ProxyAddr}} data={{.DataDir}} now={{.Now}}"
	if err := validateWorkerTemplateBody(valid); err != nil {
		t.Fatalf("valid WorkerVars rejected: %v", err)
	}
	if err := validateWorkerTemplateBody("{{.Goal}}"); err == nil {
		t.Fatal("Planner-only variable must not pass Worker template validation")
	}
	if err := validateWorkerTemplateBody(""); err == nil {
		t.Fatal("empty Worker template must be rejected")
	}
}
