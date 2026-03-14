package framework

import (
	"errors"
	"testing"
)

type testPlugin struct{ name string }

func (p testPlugin) Name() string { return p.name }

type greeter interface {
	Greet() (string, error)
}

type greeterPlugin struct {
	testPlugin
	greeting string
}

func (p greeterPlugin) Greet() (string, error) { return p.greeting, nil }

type nilableGreeter struct {
	testPlugin
	greeting *string
}

func (p nilableGreeter) Greet() (string, error) {
	if p.greeting == nil {
		return "", nil
	}
	return *p.greeting, nil
}

type counter interface {
	Count() error
}

type counterPlugin struct {
	testPlugin
	total *int
}

func (p counterPlugin) Count() error { *p.total++; return nil }

func TestCallFirst_ReturnsFirstNonNil(t *testing.T) {
	hello := "hello"
	r := NewHookRegistry()
	r.Register(nilableGreeter{testPlugin{"a"}, nil})
	r.Register(nilableGreeter{testPlugin{"b"}, &hello})
	r.Register(nilableGreeter{testPlugin{"c"}, nil})

	result, err := CallFirst[greeter](r, func(h greeter) (any, error) {
		s, err := h.Greet()
		if s == "" {
			return nil, err
		}
		return s, err
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != "hello" {
		t.Fatalf("expected 'hello', got %v", result)
	}
}

func TestCallFirst_SkipsNonImplementors(t *testing.T) {
	r := NewHookRegistry()
	r.Register(testPlugin{"plain"})
	r.Register(greeterPlugin{testPlugin{"g"}, "hi"})

	result, err := CallFirst[greeter](r, func(h greeter) (any, error) {
		return h.Greet()
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != "hi" {
		t.Fatalf("expected 'hi', got %v", result)
	}
}

func TestCallFirst_ReturnsNilWhenNoImplementor(t *testing.T) {
	r := NewHookRegistry()
	r.Register(testPlugin{"plain"})

	result, err := CallFirst[greeter](r, func(h greeter) (any, error) {
		return h.Greet()
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != nil {
		t.Fatalf("expected nil, got %v", result)
	}
}

func TestCallFirst_LaterRegistrationWins(t *testing.T) {
	r := NewHookRegistry()
	r.Register(greeterPlugin{testPlugin{"first"}, "one"})
	r.Register(greeterPlugin{testPlugin{"second"}, "two"})

	result, err := CallFirst[greeter](r, func(h greeter) (any, error) {
		return h.Greet()
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != "two" {
		t.Fatalf("expected 'two' (later wins), got %v", result)
	}
}

func TestCallAll_CallsAllImplementors(t *testing.T) {
	total := 0
	r := NewHookRegistry()
	r.Register(counterPlugin{testPlugin{"a"}, &total})
	r.Register(testPlugin{"skip"})
	r.Register(counterPlugin{testPlugin{"b"}, &total})

	errs := CallAll[counter](r, func(h counter) error { return h.Count() })
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if total != 2 {
		t.Fatalf("expected 2 calls, got %d", total)
	}
}

func TestCallAll_CollectsErrors(t *testing.T) {
	r := NewHookRegistry()
	boom := errors.New("boom")
	r.Register(counterPlugin{testPlugin{"a"}, new(int)})

	errs := CallAll[counter](r, func(h counter) error { return boom })
	if len(errs) != 1 || !errors.Is(errs[0], boom) {
		t.Fatalf("expected [boom], got %v", errs)
	}
}
