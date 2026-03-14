package framework

import "github.com/zhu327/acpclaw/internal/domain"

// Plugin is the base interface all plugins must implement.
type Plugin interface {
	Name() string
}

// HookRegistry holds registered plugins and dispatches hook calls.
type HookRegistry struct {
	plugins []Plugin
}

// NewHookRegistry creates an empty registry.
func NewHookRegistry() *HookRegistry {
	return &HookRegistry{}
}

// Register adds a plugin. Later registrations have higher priority.
func (r *HookRegistry) Register(p Plugin) {
	r.plugins = append(r.plugins, p)
}

// Plugins returns all registered plugins (for inspection/testing).
func (r *HookRegistry) Plugins() []Plugin {
	return r.plugins
}

// CallFirst iterates plugins in reverse registration order (latest first),
// calls fn on each that implements T, returns the first non-zero result.
// Returns (nil, nil) if no implementor returns a non-zero value.
func CallFirst[T any](r *HookRegistry, fn func(T) (any, error)) (any, error) {
	for i := len(r.plugins) - 1; i >= 0; i-- {
		if h, ok := r.plugins[i].(T); ok {
			result, err := fn(h)
			if err != nil {
				return nil, err
			}
			if result != nil && result != "" {
				return result, nil
			}
		}
	}
	return nil, nil
}

// CallAll iterates plugins in registration order, calls fn on each that
// implements T, collects and returns any errors.
func CallAll[T any](r *HookRegistry, fn func(T) error) []error {
	var errs []error
	for _, p := range r.plugins {
		if h, ok := p.(T); ok {
			if err := fn(h); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errs
}

// CallAllFaultIsolated is like CallAll but catches panics in each call,
// used for ErrorObserver where one failure must not block others.
func CallAllFaultIsolated[T any](r *HookRegistry, fn func(T) error) {
	for _, p := range r.plugins {
		if h, ok := p.(T); ok {
			func() {
				defer func() { recover() }()
				_ = fn(h)
			}()
		}
	}
}

// InitPlugins calls Init(fw) on any plugin implementing domain.PluginInitializer.
func (r *HookRegistry) InitPlugins(fw any) error {
	for _, p := range r.plugins {
		if pi, ok := p.(domain.PluginInitializer); ok {
			if err := pi.Init(fw); err != nil {
				return err
			}
		}
	}
	return nil
}
