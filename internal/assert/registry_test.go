package assert

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRegistry_BuildUnknownName(t *testing.T) {
	_, err := Build("does_not_exist", &yaml.Node{})
	if err == nil {
		t.Fatal("expected error for unregistered name, got nil")
	}
}

func TestRegistry_RegisterAndBuild(t *testing.T) {
	const name = "registry_test_dummy"

	Register(name, func(node *yaml.Node) (Assertion, error) {
		return &snapshotAge{maxAge: 0}, nil
	})

	a, err := Build(name, &yaml.Node{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a == nil {
		t.Fatal("expected non-nil Assertion")
	}
}

func TestRegistry_DuplicateRegisterPanics(t *testing.T) {
	const name = "registry_test_dup"
	Register(name, func(node *yaml.Node) (Assertion, error) {
		return &snapshotAge{}, nil
	})

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on duplicate registration, got none")
		}
	}()
	Register(name, func(node *yaml.Node) (Assertion, error) {
		return &snapshotAge{}, nil
	})
}
