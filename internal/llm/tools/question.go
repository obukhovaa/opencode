package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/opencode-ai/opencode/internal/question"
)

const QuestionToolName = "question"

const questionDescription = `Use this tool when you need to ask the user questions during execution. This allows you to:
1. Gather user preferences or requirements
2. Clarify ambiguous instructions
3. Get decisions on implementation choices as you work
4. Offer choices to the user about what direction to take

Usage notes:
- When ` + "`custom`" + ` is enabled (default), a "Type your own answer" option is added automatically; don't include "Other" or catch-all options
- Answers are returned as arrays of labels; set ` + "`multiple: true`" + ` to allow selecting more than one
- If you recommend a specific option, make that the first option in the list and add "(Recommended)" at the end of the label`

type questionTool struct {
	service question.Service
}

type questionParams struct {
	Questions []question.Prompt `json:"questions"`
}

func (q *questionTool) Info() ToolInfo {
	return ToolInfo{
		Name:        QuestionToolName,
		Description: questionDescription,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"questions": map[string]any{
					"type":        "array",
					"description": "Questions to ask the user",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"question": map[string]any{
								"type":        "string",
								"description": "Complete question text",
							},
							"options": map[string]any{
								"type":        "array",
								"description": "Available choices",
								"items": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"label": map[string]any{
											"type":        "string",
											"description": "Display text (1-5 words, concise)",
										},
										"description": map[string]any{
											"type":        "string",
											"description": "Explanation of choice",
										},
									},
									"required": []string{"label", "description"},
								},
							},
							"multiple": map[string]any{
								"type":        "boolean",
								"description": "Allow selecting multiple choices (default: false)",
							},
							"custom": map[string]any{
								"type":        "boolean",
								"description": "Allow typing a custom answer (default: true)",
							},
						},
						"required": []string{"question", "options"},
					},
				},
			},
			"required": []string{"questions"},
		},
		Required: []string{"questions"},
	}
}

func (q *questionTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var params questionParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Invalid parameters: %s", err.Error())), nil
	}

	if len(params.Questions) == 0 {
		return NewTextErrorResponse("At least one question is required"), nil
	}

	// Validate each question has options or custom enabled
	for i, prompt := range params.Questions {
		if len(prompt.Options) == 0 && !prompt.IsCustomEnabled() {
			return NewTextErrorResponse(fmt.Sprintf("Question %d has no options and custom answers are disabled", i+1)), nil
		}
	}

	sessionID, _ := GetContextValues(ctx)
	if sessionID == "" {
		return NewTextErrorResponse("No active session"), nil
	}

	answers, err := q.service.Ask(ctx, sessionID, params.Questions)
	if err != nil {
		return NewTextErrorResponse("The user dismissed the question"), nil
	}

	// Format answers
	parts := make([]string, 0, len(params.Questions))
	for i, prompt := range params.Questions {
		var answerText string
		if i < len(answers) && len(answers[i]) > 0 {
			answerText = strings.Join(answers[i], ", ")
		} else {
			answerText = "Unanswered"
		}
		parts = append(parts, fmt.Sprintf("%q=%q", prompt.Question, answerText))
	}

	title := fmt.Sprintf("Asked %d question", len(params.Questions))
	if len(params.Questions) > 1 {
		title += "s"
	}

	return WithResponseMetadata(
		NewTextResponse(fmt.Sprintf("User has answered your questions: %s. You can now continue with the user's answers in mind.", strings.Join(parts, ", "))),
		map[string]any{"title": title},
	), nil
}

func (q *questionTool) AllowParallelism(_ ToolCall, _ []ToolCall) bool {
	return false
}

func (q *questionTool) IsBaseline() bool {
	return true
}

func NewQuestionTool(service question.Service) BaseTool {
	return &questionTool{service: service}
}
