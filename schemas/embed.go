package schemas

import _ "embed"

//go:embed llm_extraction_prompt.md
var LLMExtractionPrompt string

//go:embed billing_ruleset.schema.json
var BillingRuleSetSchema []byte
