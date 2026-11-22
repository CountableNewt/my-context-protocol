package runtime

import (
	"encoding/json"
	"fmt"
	"github.com/CountableNewt/my-context-protocol/services/go/identity"
	"sort"
	"strings"
	"unicode"
)

const InlineLimit = 65536

var canonicalEvents = map[string]bool{"issue_discovered": true, "non_blocking_issue_discovered": true, "follow_up_work_identified": true, "blocker_encountered": true, "test_failed": true, "task_completed": true, "skill_inadequate": true, "skill_ambiguous": true, "instruction_conflict_detected": true, "security_concern_detected": true}

type Context struct {
	User         string `json:"user,omitempty"`
	Organization string `json:"organization,omitempty"`
	Workspace    string `json:"workspace,omitempty"`
	Repository   string `json:"repository,omitempty"`
	Task         string `json:"task,omitempty"`
}
type Tool struct {
	Server      string `json:"server"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	InputSchema string `json:"inputSchema,omitempty"`
	Provider    string `json:"provider,omitempty"`
}
type ResolveInput struct {
	Request         string   `json:"request"`
	Event           string   `json:"event,omitempty"`
	Context         Context  `json:"context,omitempty"`
	CurrentSkillIDs []string `json:"current_skill_ids,omitempty"`
	AvailableTools  []Tool   `json:"available_tools,omitempty"`
}
type Binding struct {
	SkillID        string   `json:"skillId,omitempty"`
	Capability     string   `json:"capability"`
	Required       bool     `json:"required"`
	SelectedServer *string  `json:"selectedServer,omitempty"`
	SelectedTool   *string  `json:"selectedTool,omitempty"`
	Confidence     float64  `json:"confidence"`
	Alternatives   []string `json:"alternatives"`
	Missing        bool     `json:"missing"`
	Fallback       string   `json:"fallback"`
	Reason         string   `json:"reason"`
}
type ResolvedSkill struct {
	ID              string  `json:"id"`
	Version         string  `json:"version"`
	ReleaseID       string  `json:"releaseId"`
	Kind            string  `json:"kind"`
	Scope           string  `json:"scope"`
	Enforcement     string  `json:"enforcement"`
	Priority        int     `json:"priority"`
	SelectionReason string  `json:"selectionReason"`
	Score           float64 `json:"score"`
	Instructions    *string `json:"instructions,omitempty"`
	ContentIncluded bool    `json:"contentIncluded"`
	ResourceURI     string  `json:"resourceUri"`
	Source          Source  `json:"source"`
}
type Conflict struct {
	SkillID       string `json:"skillId"`
	ConflictsWith string `json:"conflictsWith"`
	Unresolved    bool   `json:"unresolved"`
}
type TraceStep struct {
	SkillID string   `json:"skillId,omitempty"`
	Outcome string   `json:"outcome"`
	Reason  string   `json:"reason"`
	Score   *float64 `json:"score,omitempty"`
}
type NextAction struct {
	Order       int    `json:"order"`
	Type        string `json:"type"`
	SkillID     string `json:"skillId,omitempty"`
	Capability  string `json:"capability,omitempty"`
	Instruction string `json:"instruction"`
	ResourceURI string `json:"resourceUri,omitempty"`
}
type Resolution struct {
	SchemaVersion       int             `json:"schemaVersion"`
	TraceID             string          `json:"traceId"`
	ActiveSkills        []ResolvedSkill `json:"activeSkills"`
	SuggestedTaskSkills []ResolvedSkill `json:"suggestedTaskSkills"`
	CapabilityBindings  []Binding       `json:"capabilityBindings"`
	MissingRequirements []string        `json:"missingRequirements"`
	Conflicts           []Conflict      `json:"conflicts"`
	MissingContext      []string        `json:"missingContext"`
	EventCanonical      *bool           `json:"eventCanonical,omitempty"`
	NextActions         []NextAction    `json:"nextActions"`
	ResolutionTrace     []TraceStep     `json:"resolutionTrace"`
}

func tokens(raw string) map[string]bool {
	result := map[string]bool{}
	for _, token := range strings.FieldsFunc(strings.ToLower(raw), func(c rune) bool { return !unicode.IsLetter(c) && !unicode.IsDigit(c) }) {
		if len([]rune(token)) > 2 {
			result[token] = true
		}
	}
	return result
}
func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
func activation(mode string, current, event, intent bool) bool {
	switch mode {
	case "always":
		return true
	case "explicit":
		return current
	case "event":
		return event
	case "intent":
		return intent
	}
	return false
}
func score(d Document, query map[string]bool) float64 {
	total := 0.0
	for _, field := range []struct {
		value  string
		weight float64
	}{{d.ID, 10}, {d.Description, 5}, {strings.Join(d.Activation.Intents, " "), 7}, {strings.Join(d.Activation.Tags, " "), 6}, {strings.Join(d.Activation.Examples, " "), 4}} {
		for token := range tokens(field.value) {
			if query[token] {
				total += field.weight
			}
		}
	}
	return total
}
func avoid(conditions []string, query map[string]bool) bool {
	for _, condition := range conditions {
		terms := tokens(condition)
		match := len(terms) > 0
		for term := range terms {
			if !query[term] {
				match = false
			}
		}
		if match {
			return true
		}
	}
	return false
}
func scopeContext(scope string, c Context) bool {
	switch scope {
	case "global":
		return true
	case "organization":
		return c.Organization != ""
	case "workspace":
		return c.Workspace != ""
	case "repository":
		return c.Repository != ""
	case "task":
		return c.Task != ""
	}
	return false
}
func applies(a Assignment, project string, c Context) bool {
	target := strings.TrimSpace(a.TargetID)
	if target == "" {
		return false
	}
	if a.TargetType == "project" {
		return target == "project" || strings.EqualFold(target, project)
	}
	if a.TargetType != a.Scope {
		return false
	}
	switch a.Scope {
	case "global":
		return target == "*" || target == "global"
	case "organization":
		return c.Organization == target
	case "workspace":
		return c.Workspace == target
	case "repository":
		return c.Repository == target
	case "task":
		return c.Task == target
	}
	return false
}
func Bind(requirement Requirement, tools []Tool) Binding {
	type candidate struct {
		tool  Tool
		score int
	}
	var ranked []candidate
	parts := map[string]bool{}
	for _, term := range strings.Split(strings.ToLower(requirement.Capability), ".") {
		parts[term] = true
	}
	for _, tool := range tools {
		matched := 0
		for term := range tokens(tool.Server + " " + tool.Name + " " + tool.Description + " " + tool.Provider) {
			if parts[term] {
				matched++
			}
		}
		if matched > 0 {
			ranked = append(ranked, candidate{tool, matched})
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].tool.Name < ranked[j].tool.Name
	})
	result := Binding{Capability: requirement.Capability, Required: requirement.Required, Alternatives: []string{}, Missing: len(ranked) == 0, Fallback: requirement.OnMissing, Reason: "No available tool matched the abstract capability."}
	if len(ranked) > 0 {
		selected := ranked[0].tool
		result.SelectedServer = &selected.Server
		result.SelectedTool = &selected.Name
		result.Confidence = float64(ranked[0].score) / float64(max(len(parts), 1))
		if result.Confidence > 1 {
			result.Confidence = 1
		}
		result.Reason = "Matched capability terms against server, tool, provider, and description metadata."
		for _, v := range ranked[1:] {
			result.Alternatives = append(result.Alternatives, v.tool.Server+"/"+v.tool.Name)
		}
	}
	return result
}
func Resolve(snapshot Snapshot, input ResolveInput) (Resolution, error) {
	trace, err := identity.UUID()
	if err != nil {
		return Resolution{}, err
	}
	result := Resolution{SchemaVersion: 1, TraceID: trace, ActiveSkills: []ResolvedSkill{}, SuggestedTaskSkills: []ResolvedSkill{}, CapabilityBindings: []Binding{}, MissingRequirements: []string{}, Conflicts: []Conflict{}, MissingContext: []string{}, NextActions: []NextAction{}, ResolutionTrace: []TraceStep{}}
	if input.Event != "" {
		value := canonicalEvents[input.Event]
		result.EventCanonical = &value
	}
	if snapshot.ReleaseID == "" {
		result.MissingContext = []string{"activeRelease"}
		return result, nil
	}
	selectedDocs := map[string]Document{}
	query := tokens(input.Request + " " + input.Event)
	for _, row := range snapshot.Skills {
		d := row.Document
		if d == nil {
			result.ResolutionTrace = append(result.ResolutionTrace, TraceStep{SkillID: row.SkillID, Outcome: "excluded", Reason: "missing_canonical_document"})
			continue
		}
		var assignments []Assignment
		for _, a := range snapshot.Assignments {
			if a.SkillID == d.ID && applies(a, snapshot.ProjectID, input.Context) {
				assignments = append(assignments, a)
			}
		}
		sort.SliceStable(assignments, func(i, j int) bool {
			a, b := assignments[i], assignments[j]
			if a.Required != b.Required {
				return a.Required
			}
			if a.Priority != b.Priority {
				return a.Priority > b.Priority
			}
			return a.Scope < b.Scope
		})
		current := contains(input.CurrentSkillIDs, d.ID)
		event := input.Event != "" && contains(d.Activation.Events, input.Event)
		points := score(*d, query)
		activated := activation(d.Activation.Mode, current, event, points > 0)
		assigned := len(assignments) > 0 && activation(assignments[0].ActivationMode, current, event, points > 0)
		if !current && avoid(d.AvoidWhen, query) {
			result.ResolutionTrace = append(result.ResolutionTrace, TraceStep{SkillID: d.ID, Outcome: "excluded", Reason: "avoid_when_match", Score: &points})
			continue
		}
		if !current && !assigned && (d.Validation.ClarificationRequired || !scopeContext(d.Scope, input.Context) || !activated) {
			reason := "no_activation_match"
			if d.Validation.ClarificationRequired {
				reason = "clarification_required"
			} else if activated && !scopeContext(d.Scope, input.Context) {
				reason = "missing_scope_context"
			}
			result.ResolutionTrace = append(result.ResolutionTrace, TraceStep{SkillID: d.ID, Outcome: "excluded", Reason: reason, Score: &points})
			continue
		}
		blocked := false
		for _, requirement := range d.Requires {
			binding := Bind(requirement, input.AvailableTools)
			binding.SkillID = d.ID
			result.CapabilityBindings = append(result.CapabilityBindings, binding)
			if binding.Required && binding.Missing && binding.Fallback == "fail_activation" {
				blocked = true
			}
		}
		if blocked {
			result.ResolutionTrace = append(result.ResolutionTrace, TraceStep{SkillID: d.ID, Outcome: "excluded", Reason: "required_capability_missing", Score: &points})
			continue
		}
		reason := map[string]string{"always": "always_active", "intent": "intent_match", "event": "event_match", "explicit": "explicit_activation"}[d.Activation.Mode]
		if current {
			reason = "already_active"
		} else if assigned {
			reason = "explicit_assignment"
		}
		value := ResolvedSkill{ID: d.ID, Version: d.Version, ReleaseID: row.ReleaseID, Kind: d.Kind, Scope: d.Scope, Enforcement: d.Enforcement, Priority: d.Priority, SelectionReason: reason, Score: points, ContentIncluded: len(d.Instructions) <= InlineLimit, ResourceURI: ResourceURI(d.ID, "", d.Version), Source: d.Source}
		if len(assignments) > 0 {
			value.Scope = assignments[0].Scope
			value.Priority = assignments[0].Priority
			if assignments[0].Required {
				value.Enforcement = "required"
			}
		}
		if value.ContentIncluded {
			value.Instructions = &d.Instructions
		}
		if current || assigned || d.Activation.Mode == "always" || d.Kind == "operating" && d.Enforcement == "required" {
			result.ActiveSkills = append(result.ActiveSkills, value)
		} else {
			result.SuggestedTaskSkills = append(result.SuggestedTaskSkills, value)
		}
		selectedDocs[d.ID] = *d
		result.ResolutionTrace = append(result.ResolutionTrace, TraceStep{SkillID: d.ID, Outcome: "selected", Reason: reason, Score: &points})
	}
	ordered := func(values []ResolvedSkill) {
		order := map[string]int{"task": 0, "repository": 1, "workspace": 2, "organization": 3, "global": 4}
		sort.SliceStable(values, func(i, j int) bool {
			a, b := values[i], values[j]
			if a.Enforcement != b.Enforcement {
				return a.Enforcement == "required"
			}
			ae := a.SelectionReason == "already_active" || a.SelectionReason == "explicit_assignment"
			be := b.SelectionReason == "already_active" || b.SelectionReason == "explicit_assignment"
			if ae != be {
				return ae
			}
			if order[a.Scope] != order[b.Scope] {
				return order[a.Scope] < order[b.Scope]
			}
			if a.Priority != b.Priority {
				return a.Priority > b.Priority
			}
			if a.Score != b.Score {
				return a.Score > b.Score
			}
			return a.ID < b.ID
		})
	}
	ordered(result.ActiveSkills)
	ordered(result.SuggestedTaskSkills)
	missing := map[string]bool{}
	for _, b := range result.CapabilityBindings {
		if b.Missing {
			missing[b.Capability] = true
		}
	}
	for k := range missing {
		result.MissingRequirements = append(result.MissingRequirements, k)
	}
	sort.Strings(result.MissingRequirements)
	for _, skill := range append(append([]ResolvedSkill{}, result.ActiveSkills...), result.SuggestedTaskSkills...) {
		for _, other := range selectedDocs[skill.ID].ConflictsWith {
			if _, ok := selectedDocs[other]; ok {
				result.Conflicts = append(result.Conflicts, Conflict{skill.ID, other, true})
			}
		}
	}
	for _, field := range []struct{ name, value string }{{"organization", input.Context.Organization}, {"workspace", input.Context.Workspace}, {"repository", input.Context.Repository}, {"task", input.Context.Task}} {
		if field.value == "" {
			result.MissingContext = append(result.MissingContext, field.name)
		}
	}
	for _, skill := range result.ActiveSkills {
		kind, instruction := "apply_skill", fmt.Sprintf("Apply the included %s instructions before continuing.", skill.ID)
		if !skill.ContentIncluded {
			kind = "read_skill"
			instruction = fmt.Sprintf("Read the complete %s package before continuing.", skill.ID)
		}
		result.NextActions = append(result.NextActions, NextAction{Order: len(result.NextActions) + 1, Type: kind, SkillID: skill.ID, Instruction: instruction, ResourceURI: skill.ResourceURI})
	}
	sortedBindings := append([]Binding{}, result.CapabilityBindings...)
	sort.SliceStable(sortedBindings, func(i, j int) bool { return sortedBindings[i].Capability < sortedBindings[j].Capability })
	for _, b := range sortedBindings {
		if !b.Missing {
			continue
		}
		instruction := fmt.Sprintf("Report that %s is unavailable.", b.Capability)
		switch b.Fallback {
		case "fail_activation":
			instruction = fmt.Sprintf("Do not activate the dependent skill until %s is available.", b.Capability)
		case "warn":
			instruction = fmt.Sprintf("Warn that %s is unavailable before continuing.", b.Capability)
		case "return_draft":
			instruction = fmt.Sprintf("Return a draft for %s; do not claim the external action occurred.", b.Capability)
		case "request_provider_selection":
			instruction = fmt.Sprintf("Ask the user to select or connect a provider for %s.", b.Capability)
		case "continue_without_action":
			instruction = fmt.Sprintf("Continue without performing %s, and state that no action occurred.", b.Capability)
		}
		result.NextActions = append(result.NextActions, NextAction{Order: len(result.NextActions) + 1, Type: b.Fallback, SkillID: b.SkillID, Capability: b.Capability, Instruction: instruction})
	}
	sort.SliceStable(result.Conflicts, func(i, j int) bool {
		a, b := result.Conflicts[i], result.Conflicts[j]
		if a.SkillID == b.SkillID {
			return a.ConflictsWith < b.ConflictsWith
		}
		return a.SkillID < b.SkillID
	})
	for _, c := range result.Conflicts {
		result.NextActions = append(result.NextActions, NextAction{Order: len(result.NextActions) + 1, Type: "resolve_conflict", SkillID: c.SkillID, Instruction: fmt.Sprintf("Resolve the conflict between %s and %s before acting.", c.SkillID, c.ConflictsWith)})
	}
	if snapshot.Settings.TelemetryEnabled {
		result.NextActions = append(result.NextActions, NextAction{Order: len(result.NextActions) + 1, Type: "report_usage_at_task_completion", Instruction: "At task completion, report actual uses or considered skips through report_skill_usage. Missing reports remain unknown; do not send reasoning transcripts or explanatory notes."})
	}
	return result, nil
}
func DecodeResolve(arguments map[string]json.RawMessage) (ResolveInput, error) {
	normalized := map[string]json.RawMessage{}
	for key, value := range arguments {
		normalized[key] = value
	}
	for _, key := range []string{"context", "current_skill_ids", "available_tools"} {
		if raw, ok := normalized[key]; ok && len(raw) > 0 && raw[0] == '"' {
			var serialized string
			if json.Unmarshal(raw, &serialized) != nil || !json.Valid([]byte(serialized)) {
				return ResolveInput{}, fmt.Errorf("invalid serialized %s", key)
			}
			normalized[key] = json.RawMessage(serialized)
		}
	}
	arguments = normalized
	if raw, ok := arguments["current_skill_ids"]; ok && !stringList(raw) {
		return ResolveInput{}, fmt.Errorf("current_skill_ids must be an array of strings")
	}
	for _, key := range []string{"context", "current_skill_ids", "available_tools", "event"} {
		if value, ok := arguments[key]; ok && string(value) == "null" {
			return ResolveInput{}, fmt.Errorf("%s must not be null", key)
		}
	}
	if rawContext, ok := arguments["context"]; ok {
		var fields map[string]json.RawMessage
		if json.Unmarshal(rawContext, &fields) != nil {
			return ResolveInput{}, fmt.Errorf("context must be an object with string identity fields")
		}
		for _, value := range fields {
			if len(value) == 0 || value[0] != '"' {
				return ResolveInput{}, fmt.Errorf("context identity fields must be strings")
			}
		}
	}
	raw, _ := json.Marshal(arguments)
	var input ResolveInput
	if json.Unmarshal(raw, &input) != nil || strings.TrimSpace(input.Request) == "" || len(input.Request) > 65536 || len(input.Event) > 128 || len(input.AvailableTools) > 1000 || len(input.CurrentSkillIDs) > 1000 {
		return input, fmt.Errorf("invalid resolve_context arguments")
	}
	for _, field := range []struct {
		name   string
		target *string
	}{{"user", &input.Context.User}, {"organization", &input.Context.Organization}, {"workspace", &input.Context.Workspace}, {"repository", &input.Context.Repository}, {"task", &input.Context.Task}} {
		if value, ok := arguments[field.name]; ok {
			var s string
			if json.Unmarshal(value, &s) != nil {
				return input, fmt.Errorf("%s must be a string", field.name)
			}
			if strings.TrimSpace(s) != "" {
				*field.target = strings.TrimSpace(s)
			}
		}
	}
	for _, tool := range input.AvailableTools {
		if tool.Server == "" || tool.Name == "" {
			return input, fmt.Errorf("available_tools must contain server and name")
		}
	}
	return input, nil
}
