// Package output defines provider-neutral structured final-output contracts.
package output

import (
	"bytes"
	"context"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const DefaultMaxRetries = 1

// Contract describes a structured final answer the agent must produce. Schema
// uses JSON Schema. MaxRetries controls validation-repair attempts; zero uses
// DefaultMaxRetries, and a negative value disables retries.
type Contract struct {
	Schema     map[string]any
	MaxRetries int
}

// Enabled reports whether c requires structured output validation.
func (c Contract) Enabled() bool {
	return len(c.Schema) > 0
}

// RetryLimit returns the effective validation retry count.
func (c Contract) RetryLimit() int {
	if !c.Enabled() {
		return 0
	}
	if c.MaxRetries < 0 {
		return 0
	}
	if c.MaxRetries == 0 {
		return DefaultMaxRetries
	}
	return c.MaxRetries
}

// Validate checks text against c's JSON Schema. It compiles the schema on each
// call; callers validating repeatedly should use Compile and reuse Validator.
func (c Contract) Validate(ctx context.Context, text string) error {
	validator, err := c.Compile(ctx)
	if err != nil {
		return err
	}
	return validator.Validate(ctx, text)
}

// Compile prepares c for repeated validation. A disabled contract returns a
// no-op validator.
func (c Contract) Compile(ctx context.Context) (Validator, error) {
	if err := ctx.Err(); err != nil {
		return Validator{}, err
	}
	if !c.Enabled() {
		return Validator{contract: c}, nil
	}
	schema, err := compileSchema(c.Schema)
	if err != nil {
		return Validator{}, err
	}
	return Validator{contract: c, schema: schema}, nil
}

// Validator validates final answer text against a compiled Contract.
type Validator struct {
	contract Contract
	schema   *jsonschema.Schema
}

// Enabled reports whether v enforces a structured output contract.
func (v Validator) Enabled() bool {
	return v.contract.Enabled()
}

// RetryLimit returns the effective validation retry count.
func (v Validator) RetryLimit() int {
	return v.contract.RetryLimit()
}

// Validate checks text against v's compiled JSON Schema.
func (v Validator) Validate(ctx context.Context, text string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !v.Enabled() {
		return nil
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader([]byte(text)))
	if err != nil {
		return fmt.Errorf("output is not valid JSON: %w", err)
	}
	if err := v.schema.Validate(value); err != nil {
		return fmt.Errorf("output does not match schema: %w", err)
	}
	return nil
}

func compileSchema(raw map[string]any) (*jsonschema.Schema, error) {
	compiler := jsonschema.NewCompiler()
	const loc = "output.schema.json"
	if err := compiler.AddResource(loc, raw); err != nil {
		return nil, fmt.Errorf("compile output schema: add resource: %w", err)
	}
	schema, err := compiler.Compile(loc)
	if err != nil {
		return nil, fmt.Errorf("compile output schema: %w", err)
	}
	return schema, nil
}
