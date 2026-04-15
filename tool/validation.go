package tool

import (
	"bytes"
	"fmt"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type InputValidationError struct {
	ToolName  string
	ToolUseID string
	Err       error
}

func (e InputValidationError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("invalid input for tool %q", e.ToolName)
	}
	return fmt.Sprintf("invalid input for tool %q: %v", e.ToolName, e.Err)
}

func (e InputValidationError) Unwrap() error {
	return e.Err
}

func validateInput(use model.ToolUse, schema *jsonschema.Schema) error {
	if schema == nil {
		return nil
	}
	input := use.Input
	if len(input) == 0 {
		input = []byte(`{}`)
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(input))
	if err != nil {
		return InputValidationError{
			ToolName:  use.Name,
			ToolUseID: use.ID,
			Err:       fmt.Errorf("input is not valid JSON: %w", err),
		}
	}
	if err := schema.Validate(value); err != nil {
		return InputValidationError{
			ToolName:  use.Name,
			ToolUseID: use.ID,
			Err:       err,
		}
	}
	return nil
}
