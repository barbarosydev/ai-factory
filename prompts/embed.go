package prompts

import _ "embed"

//go:embed pm_system.md
var PMSystem string

//go:embed dev_system.md
var DevSystem string

//go:embed tester_system.md
var TesterSystem string

//go:embed reviewer_system.md
var ReviewerSystem string
