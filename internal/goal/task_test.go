package goal

import (
	"strings"
	"testing"
)

func TestTaskSubmissionIsBoundedContextNotAuthority(t *testing.T) {
	valid := TaskSubmission{Template: TaskTemplateOrderQuote, Intent: "为演示实现订单报价 API 与 HTTP 客户端", Context: TaskContext{Text: "输出可以直接消费的交付文件"}}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		edit func(*TaskSubmission)
	}{
		{"unknown-template", func(v *TaskSubmission) { v.Template = "arbitrary-agent/v1" }},
		{"empty-intent", func(v *TaskSubmission) { v.Intent = " \n " }},
		{"large-intent", func(v *TaskSubmission) { v.Intent = strings.Repeat("x", 4097) }},
		{"large-context", func(v *TaskSubmission) { v.Context.Text = strings.Repeat("x", 16385) }},
		{"nul-intent", func(v *TaskSubmission) { v.Intent += "\x00" }},
		{"nul-context", func(v *TaskSubmission) { v.Context.Text += "\x00" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			submission := valid
			tc.edit(&submission)
			if submission.Validate() == nil {
				t.Fatal("invalid submission accepted")
			}
		})
	}
}
