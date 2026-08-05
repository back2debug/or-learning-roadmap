package main

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// addNumbersTool defines a tool that adds two integers. The schema marks both
// parameters required and forbids extras, so the model cannot smuggle in
// unexpected fields.
func addNumbersTool() Tool {
	return Tool{
		Type: "function",
		Function: FunctionDef{
			Name:        "add_numbers",
			Description: "Add two integers together and return their sum. Call this whenever the user asks for an addition.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"num1": {"type": "integer", "description": "The first number to add"},
					"num2": {"type": "integer", "description": "The second number to add"}
				},
				"required": ["num1", "num2"],
				"additionalProperties": false
			}`),
		},
	}
}

// multiplyNumbersTool defines a tool that multiplies two integers.
func multiplyNumbersTool() Tool {
	return Tool{
		Type: "function",
		Function: FunctionDef{
			Name:        "multiply_numbers",
			Description: "Multiply two integers and return their product. Call this whenever the user asks for a multiplication.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"num1": {"type": "integer", "description": "The first factor"},
					"num2": {"type": "integer", "description": "The second factor"}
				},
				"required": ["num1", "num2"],
				"additionalProperties": false
			}`),
		},
	}
}

// binaryIntArgs is the validated argument shape shared by both tools.
// Pointers distinguish "missing" from "zero" so absent parameters are
// rejected instead of silently defaulting to 0.
type binaryIntArgs struct {
	Num1 *int `json:"num1"`
	Num2 *int `json:"num2"`
}

// decodeBinaryIntArgs strictly parses tool-call arguments. Unknown fields
// and missing parameters are errors — model output is untrusted input.
func decodeBinaryIntArgs(rawArgs string) (binaryIntArgs, error) {
	var args binaryIntArgs
	decoder := json.NewDecoder(bytes.NewReader([]byte(rawArgs)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return args, fmt.Errorf("invalid tool arguments %q: %w", rawArgs, err)
	}
	if args.Num1 == nil || args.Num2 == nil {
		return args, fmt.Errorf("tool arguments %q missing required num1/num2", rawArgs)
	}
	return args, nil
}

// executeTool dispatches a validated tool call and returns the result string
// that will be sent back to the model. Unknown tool names are rejected.
func executeTool(name, rawArgs string) (string, error) {
	args, err := decodeBinaryIntArgs(rawArgs)
	if err != nil {
		return "", err
	}
	switch name {
	case "add_numbers":
		return fmt.Sprintf("%d", *args.Num1+*args.Num2), nil
	case "multiply_numbers":
		return fmt.Sprintf("%d", *args.Num1**args.Num2), nil
	default:
		return "", fmt.Errorf("unknown tool %q", name)
	}
}
