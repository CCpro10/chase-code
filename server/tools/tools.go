package tools

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ParseToolCallsJSON 解析 LLM 输出的 JSON 字符串，支持单个对象或对象数组。
func ParseToolCallsJSON(raw string) ([]ToolCall, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("工具调用 JSON 为空")
	}

	if strings.HasPrefix(raw, "[") {
		var calls []ToolCall
		if err := json.Unmarshal([]byte(raw), &calls); err != nil {
			return nil, err
		}
		return calls, nil
	}

	var call ToolCall
	if err := json.Unmarshal([]byte(raw), &call); err != nil {
		return nil, err
	}
	return []ToolCall{call}, nil
}

// ApplyPatchToolMode 控制 apply_patch 的工具形态。
type ApplyPatchToolMode string

const (
	ApplyPatchToolModeAuto     ApplyPatchToolMode = "auto"
	ApplyPatchToolModeCustom   ApplyPatchToolMode = "custom"
	ApplyPatchToolModeFunction ApplyPatchToolMode = "function"
)

const applyPatchFunctionDescription = `Patch text in apply_patch format (*** Begin Patch ... *** End Patch).`

const applyPatchFunctionInputDescription = `Use the apply_patch tool to edit files.
Provide the full patch text in the input field.
Your patch language is a stripped-down, file-oriented diff format designed to be easy to parse and safe to apply. You can think of it as a high-level envelope:

*** Begin Patch
[ one or more file sections ]
*** End Patch

Within that envelope, you get a sequence of file operations.
You MUST include a header to specify the action you are taking.
Each operation starts with one of three headers:

*** Add File: <path> - create a new file. Every following line is a + line (the initial contents).
*** Delete File: <path> - remove an existing file. Nothing follows.
*** Update File: <path> - patch an existing file in place (optionally with a rename).

May be immediately followed by *** Move to: <new path> if you want to rename the file.
Then one or more hunks, each introduced by @@ (optionally followed by a hunk header).
Within a hunk each line starts with:

For instructions on [context_before] and [context_after]:
- By default, show 3 lines of code immediately above and 3 lines immediately below each change. If a change is within 3 lines of a previous change, do NOT duplicate the first change's [context_after] lines in the second change's [context_before] lines.
- If 3 lines of context is insufficient to uniquely identify the snippet of code within the file, use the @@ operator to indicate the class or function to which the snippet belongs. For instance, we might have:
@@ class BaseClass
[3 lines of pre-context]
- [old_code]
+ [new_code]
[3 lines of post-context]

- If a code block is repeated so many times in a class or function such that even a single @@ statement and 3 lines of context cannot uniquely identify the snippet of code, you can use multiple @@ statements to jump to the right context. For instance:

@@ class BaseClass
@@   def method():
[3 lines of pre-context]
- [old_code]
+ [new_code]
[3 lines of post-context]

The full grammar definition is below:
Patch := Begin { FileOp } End
Begin := "*** Begin Patch" NEWLINE
End := "*** End Patch" NEWLINE
FileOp := AddFile | DeleteFile | UpdateFile
AddFile := "*** Add File: " path NEWLINE { "+" line NEWLINE }
DeleteFile := "*** Delete File: " path NEWLINE
UpdateFile := "*** Update File: " path NEWLINE [ MoveTo ] { Hunk }
MoveTo := "*** Move to: " newPath NEWLINE
Hunk := "@@" [ header ] NEWLINE { HunkLine } [ "*** End of File" NEWLINE ]
HunkLine := (" " | "-" | "+") text NEWLINE

A full patch can combine several operations:

*** Begin Patch
*** Add File: hello.txt
+Hello world
*** Update File: src/app.py
*** Move to: src/main.py
@@ def greet():
-print("Hi")
+print("Hello, world!")
*** Delete File: obsolete.txt
*** End Patch

It is important to remember:

- You must include a header with your intended action (Add/Delete/Update)
- You must prefix new lines with + even when creating a new file
- File references can only be relative, NEVER ABSOLUTE.
`

// DefaultToolSpecs 返回 chase-code 默认暴露给 LLM 的工具集合。
var (
	toolParamsShellCommand = json.RawMessage(`{
  "type": "object",
  "properties": {
    "command": {
      "type": "string",
      "description": "The shell script to execute in the user's default shell"
    },
    "justification": {
      "type": "string",
      "description": "Only set if sandbox_permissions is \"require_escalated\". 1-sentence explanation of why we want to run this command."
    },
    "login": {
      "type": "boolean",
      "description": "Whether to run the shell with login shell semantics. Defaults to true."
    },
    "sandbox_permissions": {
      "type": "string",
      "description": "Sandbox permissions for the command. Set to \"require_escalated\" to request running without sandbox restrictions; defaults to \"use_default\"."
    },
    "timeout_ms": {
      "type": "number",
      "description": "The timeout for the command in milliseconds"
    },
    "workdir": {
      "type": "string",
      "description": "The working directory to execute the command in"
    }
  },
  "required": ["command"],
  "additionalProperties": false
}`)

	toolParamsApplyPatch = func() json.RawMessage {
		params := map[string]any{
			"type": "object",
			"properties": map[string]any{
				"input": map[string]any{
					"type":        "string",
					"description": applyPatchFunctionInputDescription,
				},
			},
			"required":             []string{"input"},
			"additionalProperties": false,
		}
		data, err := json.Marshal(params)
		if err != nil {
			return json.RawMessage(`{"type":"object","properties":{"input":{"type":"string","description":"apply_patch input"}},"required":["input"],"additionalProperties":false}`)
		}
		return data
	}()

	toolFormatApplyPatch = json.RawMessage(`{
  "type": "grammar",
  "syntax": "lark",
  "definition": "start: begin_patch hunk+ end_patch\nbegin_patch: \"*** Begin Patch\" LF\nend_patch: \"*** End Patch\" LF?\n\nhunk: add_hunk | delete_hunk | update_hunk\nadd_hunk: \"*** Add File: \" filename LF add_line+\ndelete_hunk: \"*** Delete File: \" filename LF\nupdate_hunk: \"*** Update File: \" filename LF change_move? change?\n\nfilename: /(.+)/\nadd_line: \"+\" /(.*)/ LF -> line\n\nchange_move: \"*** Move to: \" filename LF\nchange: (change_context | change_line)+ eof_line?\nchange_context: (\"@@\" | \"@@ \" /(.+)/) LF\nchange_line: (\"+\" | \"-\" | \" \") /(.*)/ LF\neof_line: \"*** End of File\" LF\n\n%import common.LF\n"
}`)
)

// ShellCommandToolSpec returns the shell_command tool definition.
func ShellCommandToolSpec() ToolSpec {
	shellStrict := false
	return ToolSpec{
		Kind:        ToolKindFunction,
		Name:        "shell_command",
		Description: "Runs a shell command and returns its output.\n- Always set the `workdir` param when using the shell_command function. Do not use `cd` unless absolutely necessary.",
		Parameters:  toolParamsShellCommand,
		Strict:      &shellStrict,
	}
}

// ApplyPatchToolSpecCustom returns the freeform apply_patch tool definition.
func ApplyPatchToolSpecCustom() ToolSpec {
	return ToolSpec{
		Kind:        ToolKindCustom,
		Name:        "apply_patch",
		Description: "Use the `apply_patch` tool to edit files. This is a FREEFORM tool, so do not wrap the patch in JSON.",
		Format:      toolFormatApplyPatch,
	}
}

// ApplyPatchToolSpecFunction returns the function-based apply_patch tool definition.
func ApplyPatchToolSpecFunction() ToolSpec {
	applyPatchStrict := false
	return ToolSpec{
		Kind:        ToolKindFunction,
		Name:        "apply_patch",
		Description: applyPatchFunctionDescription,
		Parameters:  toolParamsApplyPatch,
		Strict:      &applyPatchStrict,
	}
}

// ToolSpecsWithApplyPatchMode builds the tool list using the requested apply_patch mode.
func ToolSpecsWithApplyPatchMode(mode ApplyPatchToolMode) []ToolSpec {
	tools := []ToolSpec{ShellCommandToolSpec()}
	switch mode {
	case ApplyPatchToolModeFunction:
		tools = append(tools, ApplyPatchToolSpecFunction())
	default:
		tools = append(tools, ApplyPatchToolSpecCustom())
	}
	return tools
}

func DefaultToolSpecs() []ToolSpec {
	return ToolSpecsWithApplyPatchMode(ApplyPatchToolModeCustom)
}

// ToolCall 描述一条来自 LLM 的工具调用请求，对应约定的 JSON 协议。
type ToolCall struct {
	ToolName  string          `json:"tool_name"`
	Arguments json.RawMessage `json:"arguments"`
	CallID    string          `json:"call_id,omitempty"`
}

// ToolSpec 描述一个可被模型调用的工具定义。
// 对于当前实现，主要依赖 Name 和 Description。
type ToolSpec struct {
	Kind        ToolKind        `json:"kind"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Format      json.RawMessage `json:"format,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

// ToolKind 对应工具的类别，参考 codex 的 ToolSpec。
type ToolKind string

const (
	ToolKindFunction   ToolKind = "function"
	ToolKindLocalShell ToolKind = "local_shell"
	ToolKindWebSearch  ToolKind = "web_search"
	ToolKindCustom     ToolKind = "custom"
)
